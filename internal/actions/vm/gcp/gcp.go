// Package gcp launches single-use Actions runner VMs on Compute Engine in the
// customer's project. Hakopod's own Google identity impersonates the
// customer's service account; no service account key is configured here.
package gcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/actions/vm"
)

type Config struct {
	Installation   string
	Project        string
	Region         string
	Zones          []string
	Subnetwork     string // a subnetwork name in Region
	ServiceAccount string // impersonated; needs only the documented role
	Architecture   string
	DiskGiB        int64
}

// Client speaks the Compute Engine REST API directly, so Google's SDK
// modules stay out of go.mod.
type Client struct {
	config  Config
	http    *http.Client
	compute string
	token   interface {
		Token(context.Context) (string, error)
	}
}

// New impersonates the capacity's service account from Hakopod's own
// Google identity. Tokens are cached and refreshed before they expire.
func New(_ context.Context, c Config) (*Client, error) {
	if c.Installation == "" || c.Project == "" || c.Region == "" || len(c.Zones) == 0 || c.Subnetwork == "" || c.ServiceAccount == "" {
		return nil, errors.New("an installation, project, region, zones, subnetwork and service account are required")
	}
	h := &http.Client{Timeout: 2 * time.Minute}
	return &Client{config: c, http: h, compute: "https://compute.googleapis.com/compute/v1",
		token: impersonate(h, identity(h), "https://iamcredentials.googleapis.com", c.ServiceAccount)}, nil
}

// apiError keeps Google's status and reason and drops the message, which
// can echo request details.
type apiError struct {
	Code   int
	Status string
	Reason string
}

func (e *apiError) Error() string {
	text := strconv.Itoa(e.Code)
	if e.Status != "" {
		text += " " + e.Status
	}
	if e.Reason != "" {
		text += " (" + e.Reason + ")"
	}
	return text
}

// do calls path under the project; a path starting with "projects/" is
// absolute, for public images.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	token, err := c.token.Token(ctx)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(path, "projects/") {
		path = "projects/" + c.config.Project + "/" + path
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.compute+"/"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Status string `json:"status"`
				Errors []struct {
					Reason string `json:"reason"`
				} `json:"errors"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		failure := &apiError{Code: resp.StatusCode, Status: e.Error.Status}
		if len(e.Error.Errors) > 0 {
			failure.Reason = e.Error.Errors[0].Reason
		}
		return failure
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func notFound(err error) bool {
	var e *apiError
	return errors.As(err, &e) && e.Code == http.StatusNotFound
}

// operation is a zonal or global long-running operation.
type operation struct {
	Name   string `json:"name"`
	Zone   string `json:"zone"`
	Status string `json:"status"`
	Error  *struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	} `json:"error"`
}

// wait blocks until op is done. Its error is the first error code.
func (c *Client) wait(ctx context.Context, op operation) error {
	for op.Status != "DONE" {
		path := "global/operations/" + op.Name + "/wait"
		if op.Zone != "" {
			path = "zones/" + last(op.Zone) + "/operations/" + op.Name + "/wait"
		}
		if err := c.do(ctx, http.MethodPost, path, nil, &op); err != nil {
			return err
		}
	}
	if op.Error != nil && len(op.Error.Errors) > 0 {
		return &apiError{Status: "OPERATION_FAILED", Reason: op.Error.Errors[0].Code}
	}
	return nil
}

func last(link string) string {
	return link[strings.LastIndex(link, "/")+1:]
}

// Label values allow lowercase letters, digits, _ and -, up to 63. Tag
// values that do not fit, such as a pool's application/service or an image
// path, are labelled with a hash; the exact tags live in instance metadata.
var labelValue = regexp.MustCompile(`^[a-z0-9_-]{0,63}$`)

func label(v string) string {
	if labelValue.MatchString(v) {
		return v
	}
	sum := sha256.Sum256([]byte(v))
	return "h" + hex.EncodeToString(sum[:])[:40]
}

func labels(tags map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range tags {
		out[k] = label(v)
	}
	return out
}

const tagsKey = "hakopod-tags"

type item struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type gceInstance struct {
	Name              string            `json:"name"`
	Zone              string            `json:"zone"`
	MachineType       string            `json:"machineType"`
	Status            string            `json:"status"`
	CreationTimestamp time.Time         `json:"creationTimestamp"`
	Labels            map[string]string `json:"labels"`
	LabelFingerprint  string            `json:"labelFingerprint"`
	Metadata          struct {
		Fingerprint string `json:"fingerprint"`
		Items       []item `json:"items"`
	} `json:"metadata"`
	Scheduling struct {
		ProvisioningModel string `json:"provisioningModel"`
	} `json:"scheduling"`
	preempted bool
}

func (i gceInstance) tags() map[string]string {
	tags := map[string]string{}
	for _, m := range i.Metadata.Items {
		if m.Key == tagsKey {
			_ = json.Unmarshal([]byte(m.Value), &tags)
		}
	}
	return tags
}

// Instance states use EC2's names. A runner powers itself off at the end of
// its job and stays stopped until the slot deletes it.
var states = map[string]string{
	"PROVISIONING": "pending", "STAGING": "pending", "REPAIRING": "pending", "RUNNING": "running",
	"STOPPING": "stopping", "SUSPENDING": "stopping", "TERMINATED": "stopped", "SUSPENDED": "stopped",
}

func (i gceInstance) instance() vm.Instance {
	lifecycle := "on-demand"
	if i.Scheduling.ProvisioningModel == "SPOT" {
		lifecycle = "spot"
	}
	out := vm.Instance{ID: last(i.Zone) + "/" + i.Name, Type: last(i.MachineType), Lifecycle: lifecycle,
		State: states[i.Status], LaunchedAt: i.CreationTimestamp, Tags: i.tags()}
	if i.preempted {
		out.StateReason = "Server.SpotInstanceTermination"
	}
	return out
}

// split checks that an instance ID names one of the capacity's zones.
func (c *Client) split(id string) (zone, name string, err error) {
	zone, name, ok := strings.Cut(id, "/")
	if !ok || !slices.Contains(c.config.Zones, zone) || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("instance %s is not in this capacity's zones", id)
	}
	return zone, name, nil
}

// Instance names are lowercase, at most 63 characters, unique per zone.
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

// Hyperdisk-only machine series refuse Persistent Disk boot volumes.
func diskType(machine string) string {
	switch strings.SplitN(machine, "-", 2)[0] {
	case "c4", "c4a", "c4d", "n4", "n4a", "x4", "m4", "h4d", "a4", "a4x":
		return "hyperdisk-balanced"
	}
	return "pd-balanced"
}

// Canonical publishes Ubuntu 24.04 as an image family per architecture.
func ubuntuImage(architecture string) string {
	return "projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-" + architecture
}

// capacityReasons are operation errors that mean "try another zone or type".
var capacityReasons = []string{"ZONE_RESOURCE_POOL_EXHAUSTED", "ZONE_RESOURCE_POOL_EXHAUSTED_WITH_DETAILS",
	"QUOTA_EXCEEDED", "UNSUPPORTED_OPERATION", "VM_MIN_COUNT_NOT_REACHED", "resourceNotFound"}

// Launch tries each machine type in each zone, in order, until Compute
// Engine creates the instance. Insert operations report a lack of capacity
// only when done, so each attempt waits for its operation. The boot script
// goes in user-data, which cloud-init runs; the exact tags go next to it.
func (c *Client) Launch(ctx context.Context, in vm.LaunchInput) (vm.Instance, error) {
	tags := map[string]string{vm.TagInstallation: c.config.Installation}
	for k, v := range in.Tags {
		tags[k] = v
	}
	if len(in.InstanceTypes) == 0 || in.Name == "" {
		return vm.Instance{}, errors.New("a launch needs instance types and a name")
	}
	name, err := instanceName(in.Name)
	if err != nil {
		return vm.Instance{}, err
	}
	image, disk := in.Image, in.DiskGiB
	if image == "" {
		image = ubuntuImage(c.config.Architecture)
	}
	if disk == 0 {
		disk = c.config.DiskGiB
	}
	encoded, _ := json.Marshal(tags)
	scheduling := map[string]any{"provisioningModel": "STANDARD", "automaticRestart": false, "onHostMaintenance": "TERMINATE"}
	lifecycle := "on-demand"
	if in.Spot {
		// A reclaimed spot VM is stopped, not deleted, so its preemption can
		// still be read; the slot deletes it.
		scheduling = map[string]any{"provisioningModel": "SPOT", "instanceTerminationAction": "STOP", "automaticRestart": false, "onHostMaintenance": "TERMINATE"}
		lifecycle = "spot"
	}
	codes := []string{}
	for _, t := range in.InstanceTypes {
		for _, zone := range c.config.Zones {
			body := map[string]any{
				"name":        name,
				"machineType": "zones/" + zone + "/machineTypes/" + t,
				"labels":      labels(tags),
				"metadata": map[string]any{"items": []item{{"user-data", in.UserData}, {tagsKey, string(encoded)},
					{"block-project-ssh-keys", "true"}}},
				"disks": []map[string]any{{"boot": true, "autoDelete": true, "initializeParams": map[string]any{
					"diskName": name, "sourceImage": image, "diskSizeGb": strconv.FormatInt(disk, 10),
					"diskType": "zones/" + zone + "/diskTypes/" + diskType(t), "labels": labels(tags)}}},
				// No service account: the VM holds no Google credential.
				"networkInterfaces": []map[string]any{{
					"subnetwork":    "projects/" + c.config.Project + "/regions/" + c.config.Region + "/subnetworks/" + c.config.Subnetwork,
					"accessConfigs": []map[string]string{{"type": "ONE_TO_ONE_NAT", "name": "External NAT"}},
				}},
				"scheduling": scheduling,
			}
			var op operation
			err = c.do(ctx, http.MethodPost, "zones/"+zone+"/instances", body, &op)
			if err == nil {
				err = c.wait(ctx, op)
			}
			if err == nil {
				return vm.Instance{ID: zone + "/" + name, Type: t, Lifecycle: lifecycle, State: "pending", Tags: tags}, nil
			}
			var api *apiError
			if !errors.As(err, &api) || !slices.Contains(capacityReasons, api.Reason) {
				return vm.Instance{}, gcpError("create the instance", err)
			}
			if !slices.Contains(codes, api.Reason) {
				codes = append(codes, api.Reason)
			}
		}
	}
	return vm.Instance{}, fmt.Errorf("%w (%s)", vm.ErrNoCapacity, strings.Join(codes, ", "))
}

func (c *Client) get(ctx context.Context, id string) (gceInstance, error) {
	zone, name, err := c.split(id)
	if err != nil {
		return gceInstance{}, err
	}
	var i gceInstance
	err = c.do(ctx, http.MethodGet, "zones/"+zone+"/instances/"+name, nil, &i)
	if notFound(err) {
		return i, vm.ErrNotFound
	}
	return i, gcpError("read the instance", err)
}

func (c *Client) State(ctx context.Context, id string) (vm.Instance, error) {
	i, err := c.get(ctx, id)
	if err != nil {
		return vm.Instance{}, err
	}
	found := []gceInstance{i}
	if err = c.preempted(ctx, found); err != nil {
		return vm.Instance{}, err
	}
	return found[0].instance(), nil
}

// preempted marks spot instances Compute Engine reclaimed. The instance
// keeps no reason; its zone records a compute.instances.preempted operation
// from the moment the 30-second notice starts.
func (c *Client) preempted(ctx context.Context, found []gceInstance) error {
	zones := map[string]bool{}
	for _, i := range found {
		if i.Scheduling.ProvisioningModel == "SPOT" && i.Status != "PROVISIONING" && i.Status != "STAGING" {
			zones[last(i.Zone)] = true
		}
	}
	taken := map[string]bool{}
	for zone := range zones {
		// ponytail: one page of recent preemptions per zone; a sweep sees far fewer.
		var out struct {
			Items []struct {
				TargetLink string `json:"targetLink"`
			} `json:"items"`
		}
		filter := url.Values{"filter": {`operationType="compute.instances.preempted"`}, "maxResults": {"500"}}
		if err := c.do(ctx, http.MethodGet, "zones/"+zone+"/operations?"+filter.Encode(), nil, &out); err != nil {
			return gcpError("read the preemptions", err)
		}
		for _, op := range out.Items {
			taken[zone+"/"+last(op.TargetLink)] = true
		}
	}
	for n := range found {
		found[n].preempted = taken[last(found[n].Zone)+"/"+found[n].Name]
	}
	return nil
}

// List returns instances carrying every given tag and this installation's.
// Labels narrow the listing; the exact tags decide.
func (c *Client) List(ctx context.Context, tags map[string]string) ([]vm.Instance, error) {
	want := map[string]string{vm.TagInstallation: c.config.Installation}
	for k, v := range tags {
		want[k] = v
	}
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	filter := ""
	for _, k := range keys {
		filter += fmt.Sprintf("(labels.%s = %q) ", k, label(want[k]))
	}
	matched := []gceInstance{}
	page := ""
	for {
		q := url.Values{"filter": {strings.TrimSpace(filter)}, "maxResults": {"500"}, "returnPartialSuccess": {"true"}}
		if page != "" {
			q.Set("pageToken", page)
		}
		var out struct {
			Items map[string]struct {
				Instances []gceInstance `json:"instances"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.do(ctx, http.MethodGet, "aggregated/instances?"+q.Encode(), nil, &out); err != nil {
			return nil, gcpError("list instances", err)
		}
		for _, scoped := range out.Items {
			for _, i := range scoped.Instances {
				got := i.tags()
				ok := slices.Contains(c.config.Zones, last(i.Zone))
				for k, v := range want {
					ok = ok && got[k] == v
				}
				if ok {
					matched = append(matched, i)
				}
			}
		}
		if page = out.NextPageToken; page == "" {
			break
		}
	}
	if err := c.preempted(ctx, matched); err != nil {
		return nil, err
	}
	out := []vm.Instance{}
	for _, i := range matched {
		out = append(out, i.instance())
	}
	return out, nil
}

// own rechecks the installation tag before any change to an instance.
func (c *Client) own(ctx context.Context, id string) (gceInstance, error) {
	i, err := c.get(ctx, id)
	if err != nil {
		return i, err
	}
	if i.tags()[vm.TagInstallation] != c.config.Installation || i.Labels[vm.TagInstallation] != label(c.config.Installation) {
		return i, fmt.Errorf("instance %s is not tagged for this installation", id)
	}
	return i, nil
}

// Terminate deletes an instance of this installation. Deleting one that is
// already being deleted is not an error; it disappears shortly.
func (c *Client) Terminate(ctx context.Context, id string) error {
	_, err := c.own(ctx, id)
	if errors.Is(err, vm.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w; not terminating it", err)
	}
	zone, name, _ := c.split(id)
	err = c.do(ctx, http.MethodDelete, "zones/"+zone+"/instances/"+name, nil, nil)
	var api *apiError
	if notFound(err) || errors.As(err, &api) && api.Reason == "resourceNotReady" {
		return nil
	}
	return gcpError("delete the instance", err)
}

// Image resolves an image family to its current image, so standbys record
// the exact image they were prepared from. hakopod-minimal is resolved by the
// caller, and runs-on-full has no Compute Engine build.
func (c *Client) Image(ctx context.Context, setting string) (string, error) {
	if setting == vm.ImageRunsOnFull {
		return "", errors.New("RunsOn publishes no Compute Engine image; use hakopod-minimal or your own image")
	}
	if !strings.Contains(setting, "/global/images/family/") {
		return setting, nil
	}
	var image struct {
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodGet, setting, nil, &image); err != nil {
		return "", gcpError("resolve the image family", err)
	}
	project, _, _ := strings.Cut(strings.TrimPrefix(setting, "projects/"), "/")
	return "projects/" + project + "/global/images/" + image.Name, nil
}

// Claim gives a stopped standby this job: its new tags and the job's user
// data, then a start. A standby that is not stopped is refused; the caller
// deletes it and falls back to a cold launch.
func (c *Client) Claim(ctx context.Context, id, userData string, tags map[string]string) error {
	current, err := c.own(ctx, id)
	if err != nil {
		return err
	}
	if current.Status != "TERMINATED" {
		return fmt.Errorf("standby %s is %s, not stopped", id, strings.ToLower(current.Status))
	}
	zone, name, _ := c.split(id)
	all := current.tags()
	for k, v := range tags {
		all[k] = v
	}
	merged := current.Labels
	if merged == nil {
		merged = map[string]string{}
	}
	for k, v := range labels(tags) {
		merged[k] = v
	}
	var op operation
	if err = c.do(ctx, http.MethodPost, "zones/"+zone+"/instances/"+name+"/setLabels",
		map[string]any{"labels": merged, "labelFingerprint": current.LabelFingerprint}, &op); err == nil {
		err = c.wait(ctx, op)
	}
	if err != nil {
		return gcpError("label the standby", err)
	}
	encoded, _ := json.Marshal(all)
	items := []item{{"user-data", userData}, {tagsKey, string(encoded)}}
	for _, m := range current.Metadata.Items {
		if m.Key != "user-data" && m.Key != tagsKey {
			items = append(items, m)
		}
	}
	op = operation{}
	if err = c.do(ctx, http.MethodPost, "zones/"+zone+"/instances/"+name+"/setMetadata",
		map[string]any{"fingerprint": current.Metadata.Fingerprint, "items": items}, &op); err == nil {
		err = c.wait(ctx, op)
	}
	if err != nil {
		return gcpError("give the standby its job", err)
	}
	return gcpError("start the standby", c.do(ctx, http.MethodPost, "zones/"+zone+"/instances/"+name+"/start", nil, nil))
}

func (c *Client) ConsoleOutput(ctx context.Context, id string) (string, error) {
	zone, name, err := c.split(id)
	if err != nil {
		return "", err
	}
	var out struct {
		Contents string `json:"contents"`
	}
	err = c.do(ctx, http.MethodGet, "zones/"+zone+"/instances/"+name+"/serialPort?port=1", nil, &out)
	return out.Contents, gcpError("read the serial console", err)
}

// CreateImage images the boot disk of a stopped instance of this
// installation. Images carry labels, and the exact tags in their description.
func (c *Client) CreateImage(ctx context.Context, instance, name string, tags map[string]string) (string, error) {
	if _, err := c.own(ctx, instance); err != nil {
		return "", err
	}
	zone, disk, _ := c.split(instance)
	all := map[string]string{vm.TagInstallation: c.config.Installation}
	for k, v := range tags {
		all[k] = v
	}
	name = strings.Trim(nameUnsafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	encoded, _ := json.Marshal(all)
	body := map[string]any{"name": name, "sourceDisk": "projects/" + c.config.Project + "/zones/" + zone + "/disks/" + disk,
		"labels": labels(all), "description": string(encoded),
		"architecture": map[string]string{"amd64": "X86_64", "arm64": "ARM64"}[c.config.Architecture]}
	if err := c.do(ctx, http.MethodPost, "global/images", body, nil); err != nil {
		return "", gcpError("create the image", err)
	}
	return "projects/" + c.config.Project + "/global/images/" + name, nil
}

type gceImage struct {
	Name              string            `json:"name"`
	Status            string            `json:"status"`
	CreationTimestamp time.Time         `json:"creationTimestamp"`
	Labels            map[string]string `json:"labels"`
	Description       string            `json:"description"`
}

func (i gceImage) tags() map[string]string {
	tags := map[string]string{}
	_ = json.Unmarshal([]byte(i.Description), &tags)
	return tags
}

func (c *Client) Images(ctx context.Context, tags map[string]string) ([]vm.Image, error) {
	want := map[string]string{vm.TagInstallation: c.config.Installation}
	for k, v := range tags {
		want[k] = v
	}
	var out struct {
		Items []gceImage `json:"items"`
	}
	q := url.Values{"filter": {fmt.Sprintf("labels.%s = %q", vm.TagInstallation, label(c.config.Installation))}, "maxResults": {"500"}}
	if err := c.do(ctx, http.MethodGet, "global/images?"+q.Encode(), nil, &out); err != nil {
		return nil, gcpError("list images", err)
	}
	state := map[string]string{"READY": "available", "PENDING": "pending", "FAILED": "failed"}
	images := []vm.Image{}
	for _, i := range out.Items {
		got, ok := i.tags(), state[i.Status] != ""
		for k, v := range want {
			ok = ok && got[k] == v
		}
		if ok {
			images = append(images, vm.Image{ID: "projects/" + c.config.Project + "/global/images/" + i.Name, State: state[i.Status], CreatedAt: i.CreationTimestamp, Tags: got})
		}
	}
	return images, nil
}

// DeleteImage deletes an image of this installation in the capacity's
// project.
func (c *Client) DeleteImage(ctx context.Context, id string) error {
	name, ok := strings.CutPrefix(id, "projects/"+c.config.Project+"/global/images/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("image %s is not in this capacity's project; not deleting it", id)
	}
	var image gceImage
	err := c.do(ctx, http.MethodGet, "global/images/"+name, nil, &image)
	if notFound(err) {
		return nil
	}
	if err != nil {
		return gcpError("read the image", err)
	}
	if image.tags()[vm.TagInstallation] != c.config.Installation || image.Labels[vm.TagInstallation] != label(c.config.Installation) {
		return fmt.Errorf("image %s is not tagged for this installation; not deleting it", id)
	}
	err = c.do(ctx, http.MethodDelete, "global/images/"+name, nil, nil)
	if notFound(err) {
		return nil
	}
	return gcpError("delete the image", err)
}

// Check impersonates the service account, reads the subnetwork and reports
// firewall rules that let the internet reach every VM on its network: a
// runner needs nothing inbound.
func (c *Client) Check(ctx context.Context) ([]string, error) {
	var subnet struct {
		Network string `json:"network"`
	}
	if err := c.do(ctx, http.MethodGet, "regions/"+c.config.Region+"/subnetworks/"+c.config.Subnetwork, nil, &subnet); err != nil {
		return nil, gcpError("read the subnetwork", err)
	}
	var rules struct {
		Items []struct {
			Name          string   `json:"name"`
			Network       string   `json:"network"`
			Direction     string   `json:"direction"`
			Disabled      bool     `json:"disabled"`
			SourceRanges  []string `json:"sourceRanges"`
			TargetTags    []string `json:"targetTags"`
			TargetAccount []string `json:"targetServiceAccounts"`
			Allowed       []any    `json:"allowed"`
		} `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "global/firewalls?maxResults=500", nil, &rules); err != nil {
		return nil, gcpError("read the firewall rules", err)
	}
	warnings := []string{}
	for _, r := range rules.Items {
		if r.Network == subnet.Network && r.Direction == "INGRESS" && !r.Disabled && len(r.Allowed) > 0 &&
			len(r.TargetTags) == 0 && len(r.TargetAccount) == 0 && slices.Contains(r.SourceRanges, "0.0.0.0/0") {
			warnings = append(warnings, fmt.Sprintf("firewall rule %s allows inbound traffic from anywhere to every VM on the network; runners need none", r.Name))
		}
	}
	return warnings, nil
}

func gcpError(action string, err error) error {
	if err == nil {
		return nil
	}
	var api *apiError
	if errors.As(err, &api) {
		return fmt.Errorf("Google Cloud could not %s: %s", action, api.Error())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, vm.ErrNotFound) {
		return err
	}
	return fmt.Errorf("Google Cloud could not %s: %v", action, err)
}
