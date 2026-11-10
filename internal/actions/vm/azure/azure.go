// Package azure launches single-use Actions runner VMs on Azure in the
// customer's resource group. Hakopod's own Azure identity holds a role there;
// no credential is configured per capacity.
package azure

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/actions/vm"
)

const (
	computeAPI = "2024-07-01"
	networkAPI = "2024-05-01"
	groupsAPI  = "2021-04-01"
)

type Config struct {
	Installation   string
	SubscriptionID string
	ResourceGroup  string
	Region         string   // an Azure location such as eastus
	Zones          []string // availability zones such as "1"; empty launches without a zone
	Subnet         string   // the full resource ID of one subnet in Region
	Architecture   string
	DiskGiB        int64
}

// Client speaks the Azure Resource Manager REST API directly, so Azure's SDK
// modules stay out of go.mod.
type Client struct {
	config Config
	http   *http.Client
	arm    string
	poll   time.Duration
	token  interface {
		Token(context.Context) (string, error)
	}
}

// New uses Hakopod's own Azure identity, which the customer granted a role on
// the capacity's resource group. Tokens are cached and refreshed before they
// expire.
func New(_ context.Context, c Config) (*Client, error) {
	if c.Installation == "" || c.SubscriptionID == "" || c.ResourceGroup == "" || c.Region == "" || c.Subnet == "" {
		return nil, errors.New("an installation, subscription, resource group, region and subnet are required")
	}
	h := &http.Client{Timeout: 2 * time.Minute}
	return &Client{config: c, http: h, arm: "https://management.azure.com", poll: 5 * time.Second,
		token: identity(h, "https://login.microsoftonline.com", "http://169.254.169.254/metadata/identity/oauth2/token")}, nil
}

var errUnsupported = errors.New("warm pools and hakopod-minimal images are not supported on Azure yet")

// apiError keeps the HTTP status and ARM's error codes and drops the
// messages, which can echo request details.
type apiError struct {
	Status int
	Codes  []string // the error code, then its details' codes
}

func (e *apiError) Error() string {
	text := strings.Join(e.Codes, ", ")
	if e.Status != 0 {
		text = strings.TrimSpace(fmt.Sprint(e.Status) + " " + text)
	}
	return text
}

type armError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"details"`
}

func (e armError) apiError(status int) *apiError {
	out := &apiError{Status: status}
	add := func(code, message string) {
		// ARM reports exhausted core quotas, spot ones included, as
		// OperationNotAllowed; only the message tells them apart.
		if code == "OperationNotAllowed" && strings.Contains(strings.ToLower(message), "quota") {
			code = "QuotaExceeded"
		}
		if code != "" {
			out.Codes = append(out.Codes, code)
		}
	}
	add(e.Code, e.Message)
	for _, d := range e.Details {
		add(d.Code, d.Message)
	}
	return out
}

// do calls a path under the ARM endpoint, or an absolute URL that ARM handed
// back (an async operation or the next page), which must be on the same host.
func (c *Client) do(ctx context.Context, method, path string, body, out any) (http.Header, error) {
	target := c.arm + path
	if strings.Contains(path, "://") {
		if !strings.HasPrefix(path, c.arm+"/") {
			return nil, fmt.Errorf("Azure returned a link outside %s", c.arm)
		}
		target = path
	}
	token, err := c.token.Token(ctx)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error armError `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return resp.Header, e.Error.apiError(resp.StatusCode)
	}
	if out == nil || len(raw) == 0 {
		return resp.Header, nil
	}
	return resp.Header, json.Unmarshal(raw, out)
}

func notFound(err error) bool {
	var e *apiError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// capacityCodes are errors that mean "try another size or zone".
var capacityCodes = []string{"SkuNotAvailable", "AllocationFailed", "ZonalAllocationFailed", "OverconstrainedAllocationRequest",
	"OverconstrainedZonalAllocationRequest", "QuotaExceeded"}

func capacityError(err error) (string, bool) {
	var api *apiError
	if !errors.As(err, &api) {
		return "", false
	}
	for _, code := range api.Codes {
		if slices.Contains(capacityCodes, code) || strings.HasPrefix(code, "SpotVM") {
			return code, true
		}
	}
	return "", false
}

func (c *Client) group() string {
	return "/subscriptions/" + url.PathEscape(c.config.SubscriptionID) + "/resourceGroups/" + url.PathEscape(c.config.ResourceGroup)
}

func (c *Client) vmPath(name string) string {
	return c.group() + "/providers/Microsoft.Compute/virtualMachines/" + url.PathEscape(name)
}

type armVM struct {
	Name       string            `json:"name"`
	Tags       map[string]string `json:"tags"`
	Properties struct {
		ProvisioningState string    `json:"provisioningState"`
		Priority          string    `json:"priority"`
		TimeCreated       time.Time `json:"timeCreated"`
		HardwareProfile   struct {
			VMSize string `json:"vmSize"`
		} `json:"hardwareProfile"`
		InstanceView *struct {
			Statuses []struct {
				Code string `json:"code"`
			} `json:"statuses"`
		} `json:"instanceView"`
	} `json:"properties"`
}

// status returns the instance view's code under prefix, such as the power
// state, or "".
func (i armVM) status(prefix string) string {
	if i.Properties.InstanceView == nil {
		return ""
	}
	for _, s := range i.Properties.InstanceView.Statuses {
		if v, ok := strings.CutPrefix(s.Code, prefix); ok {
			return v
		}
	}
	return ""
}

// Instance states use EC2's names. A runner powers itself off at the end of
// its job; Azure keeps billing a VM that is stopped but not deallocated, so
// the slot and the sweep delete it.
var powerStates = map[string]string{"starting": "pending", "creating": "pending", "running": "running", "stopping": "stopping",
	"stopped": "stopped", "deallocating": "stopped", "deallocated": "stopped"}

func (c *Client) instance(i armVM) vm.Instance {
	lifecycle := "on-demand"
	if strings.EqualFold(i.Properties.Priority, "Spot") {
		lifecycle = "spot"
	}
	power := i.status("PowerState/")
	state := powerStates[power]
	switch {
	case strings.EqualFold(i.Properties.ProvisioningState, "Deleting"):
		state = "shutting-down"
	case state == "" && strings.EqualFold(i.Properties.ProvisioningState, "Failed"):
		state = "stopped" // never ran; the sweep deletes it
	case state == "":
		state = "pending"
	}
	out := vm.Instance{ID: c.config.ResourceGroup + "/" + i.Name, Type: i.Properties.HardwareProfile.VMSize, Lifecycle: lifecycle,
		State: state, LaunchedAt: i.Properties.TimeCreated, Tags: i.Tags}
	// Hakopod never deallocates a runner, so a deallocated Spot VM was
	// evicted.
	if lifecycle == "spot" && (power == "deallocated" || power == "deallocating") {
		out.StateReason = "Server.SpotInstanceTermination"
	}
	return out
}

// split checks that an instance ID, resourceGroup/name, names a VM in the
// capacity's resource group.
func (c *Client) split(id string) (string, error) {
	group, name, ok := strings.Cut(id, "/")
	if !ok || !strings.EqualFold(group, c.config.ResourceGroup) || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("instance %s is not in this capacity's resource group", id)
	}
	return name, nil
}

// VM names are lowercase, at most 64 characters for Linux.
var nameUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

func instanceName(name string) (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	name = strings.Trim(nameUnsafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(name) > 45 {
		name = name[:45]
	}
	return "hakopod-" + name + "-" + hex.EncodeToString(suffix), nil
}

// throwawayKey returns an OpenSSH RSA public key whose private key is
// discarded: Azure requires a key when password login is off, and nobody
// logs in to a runner.
func throwawayKey() (string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	put := func(p []byte) {
		_ = binary.Write(&b, binary.BigEndian, uint32(len(p)))
		b.Write(p)
	}
	mpint := func(n *big.Int) []byte {
		p := n.Bytes()
		if len(p) > 0 && p[0]&0x80 != 0 {
			p = append([]byte{0}, p...)
		}
		return p
	}
	put([]byte("ssh-rsa"))
	put(mpint(big.NewInt(int64(key.E))))
	put(mpint(key.N))
	return "ssh-rsa " + base64.StdEncoding.EncodeToString(b.Bytes()) + " hakopod-unused", nil
}

var imageURN = regexp.MustCompile(`^[A-Za-z0-9][-A-Za-z0-9_.]{0,63}:[-A-Za-z0-9_.]{1,64}:[-A-Za-z0-9_.]{1,64}:[-A-Za-z0-9_.]{1,64}$`)

func imageReference(image string) (map[string]string, error) {
	if strings.HasPrefix(image, "/subscriptions/") {
		return map[string]string{"id": image}, nil
	}
	if !imageURN.MatchString(image) {
		return nil, fmt.Errorf("image %q is not a publisher:offer:sku:version URN or an image resource ID", image)
	}
	p := strings.Split(image, ":")
	return map[string]string{"publisher": p[0], "offer": p[1], "sku": p[2], "version": p[3]}, nil
}

// Image resolves ubuntu-24.04 to Canonical's marketplace image for the
// capacity's architecture. URNs and image resource IDs are used as given.
func (c *Client) Image(_ context.Context, setting string) (string, error) {
	switch setting {
	case vm.ImageRunsOnFull, vm.ImageHakopodMinimal:
		return "", fmt.Errorf("%s images are not supported on Azure yet; use %s, a marketplace URN or an image resource ID", setting, vm.ImageUbuntu)
	case "", vm.ImageUbuntu:
		if c.config.Architecture == "arm64" {
			return "Canonical:ubuntu-24_04-lts:server-arm64:latest", nil
		}
		return "Canonical:ubuntu-24_04-lts:server:latest", nil
	}
	if _, err := imageReference(setting); err != nil {
		return "", err
	}
	return setting, nil
}

// Launch tries each VM size in each zone, in order, until Azure creates the
// VM. Allocation failures arrive only when the create is done, so each
// attempt waits for it; a VM that failed is deleted with its NIC, public IP
// and disk. The boot script goes in customData, which cloud-init runs.
func (c *Client) Launch(ctx context.Context, in vm.LaunchInput) (vm.Instance, error) {
	tags := map[string]string{vm.TagInstallation: c.config.Installation}
	for k, v := range in.Tags {
		tags[k] = v
	}
	if len(in.InstanceTypes) == 0 || in.Name == "" {
		return vm.Instance{}, errors.New("a launch needs instance types and a name")
	}
	image, err := c.Image(ctx, in.Image)
	if err != nil {
		return vm.Instance{}, err
	}
	reference, err := imageReference(image)
	if err != nil {
		return vm.Instance{}, err
	}
	disk := in.DiskGiB
	if disk == 0 {
		disk = c.config.DiskGiB
	}
	key, err := throwawayKey()
	if err != nil {
		return vm.Instance{}, err
	}
	lifecycle := "on-demand"
	if in.Spot {
		lifecycle = "spot"
	}
	zones := c.config.Zones
	if len(zones) == 0 {
		zones = []string{""}
	}
	codes := []string{}
	for _, t := range in.InstanceTypes {
		for _, zone := range zones {
			// A failed VM is deleted in the background, so every attempt
			// gets its own name.
			name, err := instanceName(in.Name)
			if err != nil {
				return vm.Instance{}, err
			}
			header, err := c.do(ctx, http.MethodPut, c.vmPath(name)+"?api-version="+computeAPI, c.body(name, t, zone, in, tags, reference, disk, key), nil)
			created := err == nil
			if err == nil {
				err = c.wait(ctx, header, name)
			}
			if err == nil {
				return vm.Instance{ID: c.config.ResourceGroup + "/" + name, Type: t, Lifecycle: lifecycle, State: "pending", Tags: tags}, nil
			}
			if created {
				_, _ = c.do(context.WithoutCancel(ctx), http.MethodDelete, c.vmPath(name)+"?api-version="+computeAPI, nil, nil)
			}
			code, ok := capacityError(err)
			if !ok {
				return vm.Instance{}, azureError("create the VM", err)
			}
			if !slices.Contains(codes, code) {
				codes = append(codes, code)
			}
		}
	}
	return vm.Instance{}, fmt.Errorf("%w (%s)", vm.ErrNoCapacity, strings.Join(codes, ", "))
}

func (c *Client) body(name, size, zone string, in vm.LaunchInput, tags, image map[string]string, disk int64, key string) map[string]any {
	properties := map[string]any{
		"hardwareProfile": map[string]string{"vmSize": size},
		"storageProfile": map[string]any{
			"imageReference": image,
			"osDisk": map[string]any{"createOption": "FromImage", "deleteOption": "Delete", "diskSizeGB": disk,
				"managedDisk": map[string]string{"storageAccountType": "StandardSSD_LRS"}},
		},
		"osProfile": map[string]any{
			"computerName": name, "adminUsername": "hakopod",
			"customData": base64.StdEncoding.EncodeToString([]byte(in.UserData)),
			"linuxConfiguration": map[string]any{"disablePasswordAuthentication": true,
				"ssh": map[string]any{"publicKeys": []map[string]string{{"path": "/home/hakopod/.ssh/authorized_keys", "keyData": key}}}},
		},
		// The NIC and its Standard public IP live and die with the VM. A
		// Standard public IP admits nothing inbound without an NSG rule and
		// gives the runner its way out to GitHub. No managed identity: the VM
		// holds no Azure credential.
		"networkProfile": map[string]any{
			"networkApiVersion": "2020-11-01",
			"networkInterfaceConfigurations": []map[string]any{{"name": name, "properties": map[string]any{
				"primary": true, "deleteOption": "Delete",
				"ipConfigurations": []map[string]any{{"name": "ipconfig1", "properties": map[string]any{
					"primary": true, "subnet": map[string]string{"id": c.config.Subnet},
					"publicIPAddressConfiguration": map[string]any{"name": name,
						"sku":        map[string]string{"name": "Standard", "tier": "Regional"},
						"properties": map[string]any{"deleteOption": "Delete", "publicIPAllocationMethod": "Static"}},
				}}},
			}}},
		},
		"diagnosticsProfile": map[string]any{"bootDiagnostics": map[string]any{"enabled": true}},
		"priority":           "Regular",
	}
	if in.Spot {
		// A reclaimed Spot VM is deallocated, not deleted, so its eviction
		// can still be read; the slot deletes it.
		properties["priority"], properties["evictionPolicy"], properties["billingProfile"] = "Spot", "Deallocate", map[string]any{"maxPrice": -1}
	}
	body := map[string]any{"location": c.config.Region, "tags": tags, "properties": properties}
	if zone != "" {
		body["zones"] = []string{zone}
	}
	return body
}

// wait follows a create's Azure-AsyncOperation, or else the VM's own
// provisioning state, until it succeeds or fails.
func (c *Client) wait(ctx context.Context, header http.Header, name string) error {
	async := header.Get("Azure-AsyncOperation")
	for {
		if async != "" {
			var op struct {
				Status string   `json:"status"`
				Error  armError `json:"error"`
			}
			_, err := c.do(ctx, http.MethodGet, async, nil, &op)
			var api *apiError
			if errors.As(err, &api) && api.Status == http.StatusForbidden {
				// The operation lives at subscription scope, which a role
				// on the resource group may not cover; read the VM instead.
				async = ""
				continue
			}
			if err != nil {
				return err
			}
			switch op.Status {
			case "Succeeded":
				return nil
			case "Failed", "Canceled":
				return op.Error.apiError(0)
			}
		} else {
			i, err := c.get(ctx, name)
			if err != nil {
				return err
			}
			switch strings.ToLower(i.Properties.ProvisioningState) {
			case "succeeded":
				return nil
			case "failed", "canceled":
				// The instance view reads ProvisioningState/failed/<code>.
				code := i.status("ProvisioningState/failed/")
				return &apiError{Codes: []string{cmp.Or(code, "ProvisioningFailed")}}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.poll):
		}
	}
}

func (c *Client) get(ctx context.Context, name string) (armVM, error) {
	var i armVM
	_, err := c.do(ctx, http.MethodGet, c.vmPath(name)+"?api-version="+computeAPI+"&$expand=instanceView", nil, &i)
	if notFound(err) {
		return i, vm.ErrNotFound
	}
	return i, azureError("read the VM", err)
}

func (c *Client) State(ctx context.Context, id string) (vm.Instance, error) {
	name, err := c.split(id)
	if err != nil {
		return vm.Instance{}, err
	}
	i, err := c.get(ctx, name)
	if err != nil {
		return vm.Instance{}, err
	}
	return c.instance(i), nil
}

// List returns VMs of the resource group carrying every given tag and this
// installation's. The listing carries no power state, so each match is read
// with its instance view.
// ponytail: one GET per matching VM; a capacity has at most max_instances.
func (c *Client) List(ctx context.Context, tags map[string]string) ([]vm.Instance, error) {
	want := map[string]string{vm.TagInstallation: c.config.Installation}
	for k, v := range tags {
		want[k] = v
	}
	out := []vm.Instance{}
	next := c.group() + "/providers/Microsoft.Compute/virtualMachines?api-version=" + computeAPI
	for next != "" {
		var page struct {
			Value    []armVM `json:"value"`
			NextLink string  `json:"nextLink"`
		}
		if _, err := c.do(ctx, http.MethodGet, next, nil, &page); err != nil {
			return nil, azureError("list VMs", err)
		}
		for _, i := range page.Value {
			ok := true
			for k, v := range want {
				ok = ok && i.Tags[k] == v
			}
			if !ok {
				continue
			}
			full, err := c.get(ctx, i.Name)
			if errors.Is(err, vm.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, c.instance(full))
		}
		next = page.NextLink
	}
	return out, nil
}

// Terminate deletes a VM of this installation; its NIC, public IP and disk go
// with it. Deleting one that is already gone is not an error.
func (c *Client) Terminate(ctx context.Context, id string) error {
	name, err := c.split(id)
	if err != nil {
		return err
	}
	i, err := c.get(ctx, name)
	if errors.Is(err, vm.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if i.Tags[vm.TagInstallation] != c.config.Installation {
		return fmt.Errorf("instance %s is not tagged for this installation; not terminating it", id)
	}
	_, err = c.do(ctx, http.MethodDelete, c.vmPath(name)+"?api-version="+computeAPI, nil, nil)
	if notFound(err) {
		return nil
	}
	return azureError("delete the VM", err)
}

// ConsoleOutput reads the serial log that boot diagnostics keep, through a
// short-lived SAS link. The link carries its own authorization, so the
// bearer token is not sent there.
func (c *Client) ConsoleOutput(ctx context.Context, id string) (string, error) {
	name, err := c.split(id)
	if err != nil {
		return "", err
	}
	var links struct {
		Serial string `json:"serialConsoleLogBlobUri"`
	}
	if _, err = c.do(ctx, http.MethodPost, c.vmPath(name)+"/retrieveBootDiagnosticsData?api-version="+computeAPI+"&sasUriExpirationTimeInMinutes=5", nil, &links); err != nil {
		return "", azureError("read boot diagnostics", err)
	}
	if links.Serial == "" {
		return "", nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, links.Serial, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", azureError("read the serial log", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Azure could not read the serial log: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(raw), err
}

func (c *Client) Claim(context.Context, string, string, map[string]string) error {
	return errUnsupported
}

func (c *Client) CreateImage(context.Context, string, string, map[string]string) (string, error) {
	return "", errUnsupported
}

func (c *Client) Images(context.Context, map[string]string) ([]vm.Image, error) {
	return nil, errUnsupported
}

func (c *Client) DeleteImage(context.Context, string) error { return errUnsupported }

func location(v string) string {
	return strings.ToLower(strings.ReplaceAll(v, " ", ""))
}

// Check reads the resource group and the subnet with Hakopod's identity,
// refuses a subnet outside the region and reports NSG rules that let the
// internet in: a runner needs nothing inbound.
func (c *Client) Check(ctx context.Context) ([]string, error) {
	if _, err := c.do(ctx, http.MethodGet, c.group()+"?api-version="+groupsAPI, nil, nil); err != nil {
		return nil, azureError("read the resource group", err)
	}
	network, _, ok := strings.Cut(c.config.Subnet, "/subnets/")
	if !ok || !strings.HasPrefix(c.config.Subnet, "/subscriptions/") {
		return nil, errors.New("the subnet is not a subnet resource ID")
	}
	var vnet struct {
		Location string `json:"location"`
	}
	if _, err := c.do(ctx, http.MethodGet, network+"?api-version="+networkAPI, nil, &vnet); err != nil {
		return nil, azureError("read the virtual network", err)
	}
	if location(vnet.Location) != location(c.config.Region) {
		return nil, fmt.Errorf("the subnet's virtual network is in %s, not in the capacity's region %s", vnet.Location, c.config.Region)
	}
	var subnet struct {
		Properties struct {
			NetworkSecurityGroup *struct {
				ID string `json:"id"`
			} `json:"networkSecurityGroup"`
		} `json:"properties"`
	}
	if _, err := c.do(ctx, http.MethodGet, c.config.Subnet+"?api-version="+networkAPI, nil, &subnet); err != nil {
		return nil, azureError("read the subnet", err)
	}
	warnings := []string{}
	if subnet.Properties.NetworkSecurityGroup == nil {
		return warnings, nil
	}
	var nsg struct {
		Name       string `json:"name"`
		Properties struct {
			SecurityRules []struct {
				Name       string `json:"name"`
				Properties struct {
					Direction      string   `json:"direction"`
					Access         string   `json:"access"`
					SourcePrefix   string   `json:"sourceAddressPrefix"`
					SourcePrefixes []string `json:"sourceAddressPrefixes"`
				} `json:"properties"`
			} `json:"securityRules"`
		} `json:"properties"`
	}
	_, err := c.do(ctx, http.MethodGet, subnet.Properties.NetworkSecurityGroup.ID+"?api-version="+networkAPI, nil, &nsg)
	var api *apiError
	if errors.As(err, &api) && api.Status == http.StatusForbidden {
		return append(warnings, "Hakopod may not read the subnet's network security group, so its inbound rules were not checked"), nil
	}
	if err != nil {
		return nil, azureError("read the subnet's network security group", err)
	}
	for _, r := range nsg.Properties.SecurityRules {
		p := r.Properties
		if !strings.EqualFold(p.Direction, "Inbound") || !strings.EqualFold(p.Access, "Allow") {
			continue
		}
		for _, source := range append([]string{p.SourcePrefix}, p.SourcePrefixes...) {
			if source == "*" || source == "0.0.0.0/0" || strings.EqualFold(source, "Internet") {
				warnings = append(warnings, fmt.Sprintf("network security group %s rule %s allows inbound traffic from the internet to the subnet; runners need none", nsg.Name, r.Name))
				break
			}
		}
	}
	return warnings, nil
}

func azureError(action string, err error) error {
	if err == nil {
		return nil
	}
	var api *apiError
	if errors.As(err, &api) {
		return fmt.Errorf("Azure could not %s: %s", action, api.Error())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, vm.ErrNotFound) {
		return err
	}
	return fmt.Errorf("Azure could not %s: %v", action, err)
}
