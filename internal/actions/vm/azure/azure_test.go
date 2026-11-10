package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions/vm"
)

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

const (
	group  = "/subscriptions/sub/resourceGroups/rg"
	vms    = group + "/providers/Microsoft.Compute/virtualMachines"
	vnet   = "/subscriptions/sub/resourceGroups/net/providers/Microsoft.Network/virtualNetworks/vnet"
	subnet = vnet + "/subnets/runners"
	nsg    = "/subscriptions/sub/resourceGroups/net/providers/Microsoft.Network/networkSecurityGroups/open"
)

// fakeARM keeps VMs by name and records each create body.
type fakeARM struct {
	mu        sync.Mutex
	url       string
	vms       map[string]map[string]any
	power     map[string]string
	refuse    map[string]string // size/zone -> code the PUT itself refuses with
	fail      map[string]string // size/zone -> code the create operation fails with
	failed    map[string]string // VM name -> its create's failure code
	bodies    map[string]map[string]any
	deleted   []string
	location  string
	blobAuth  string
	calls     []string
	forbidOps bool // operations at subscription scope answer 403
	unexpects []string
}

func (f *fakeARM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	write := func(code int, v any) {
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	if r.URL.Path == "/blob" {
		f.blobAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("hakopod: starting the runner\n"))
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(401)
		return
	}
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	armFail := func(code int, reason string) {
		write(code, map[string]any{"error": map[string]string{"code": reason, "message": "secret detail"}})
	}
	name, isVM := strings.CutPrefix(r.URL.Path, vms+"/")
	switch {
	case r.Method == "PUT" && isVM:
		if r.URL.Query().Get("api-version") != computeAPI {
			armFail(400, "BadVersion")
			return
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		f.bodies[name] = body
		size := body["properties"].(map[string]any)["hardwareProfile"].(map[string]any)["vmSize"].(string)
		zone := ""
		if z, ok := body["zones"].([]any); ok {
			zone = z[0].(string)
		}
		if code := f.refuse[size+"/"+zone]; code != "" {
			armFail(409, code)
			return
		}
		body["name"] = name
		body["properties"].(map[string]any)["provisioningState"] = "Creating"
		body["properties"].(map[string]any)["timeCreated"] = time.Now().UTC().Format(time.RFC3339)
		f.vms[name] = body
		f.failed[name] = f.fail[size+"/"+zone]
		w.Header().Set("Azure-AsyncOperation", f.url+"/subscriptions/sub/providers/Microsoft.Compute/locations/eastus/operations/"+name)
		write(201, body)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/subscriptions/sub/providers/Microsoft.Compute/locations/eastus/operations/"):
		if f.forbidOps {
			armFail(403, "AuthorizationFailed")
			return
		}
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		props := f.vms[name]["properties"].(map[string]any)
		if code := f.failed[name]; code != "" {
			props["provisioningState"] = "Failed"
			write(200, map[string]any{"status": "Failed", "error": map[string]any{"code": "OverconstrainedAllocationRequest", "details": []map[string]string{{"code": code}}}})
			return
		}
		props["provisioningState"] = "Succeeded"
		f.power[name] = "running"
		write(200, map[string]string{"status": "Succeeded"})
	case r.Method == "GET" && isVM:
		i, ok := f.vms[name]
		if !ok {
			armFail(404, "ResourceNotFound")
			return
		}
		props := i["properties"].(map[string]any)
		if f.forbidOps && props["provisioningState"] == "Creating" {
			props["provisioningState"] = "Succeeded"
		}
		statuses := []map[string]string{{"code": "ProvisioningState/" + strings.ToLower(props["provisioningState"].(string))}}
		if p := f.power[name]; p != "" {
			statuses = append(statuses, map[string]string{"code": "PowerState/" + p})
		}
		if r.URL.Query().Get("$expand") == "instanceView" {
			props["instanceView"] = map[string]any{"statuses": statuses}
		}
		write(200, i)
		delete(props, "instanceView")
	case r.Method == "DELETE" && isVM:
		f.deleted = append(f.deleted, name)
		delete(f.vms, name)
		w.WriteHeader(202)
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/retrieveBootDiagnosticsData"):
		write(200, map[string]string{"serialConsoleLogBlobUri": f.url + "/blob?sig=x"})
	case r.Method == "GET" && r.URL.Path == vms:
		list := []any{}
		for _, name := range slices.Sorted(maps.Keys(f.vms)) {
			list = append(list, f.vms[name])
		}
		if r.URL.Query().Get("page") == "" {
			write(200, map[string]any{"value": list[:len(list)/2], "nextLink": f.url + vms + "?api-version=" + computeAPI + "&page=2"})
			return
		}
		write(200, map[string]any{"value": list[len(list)/2:]})
	case r.Method == "GET" && r.URL.Path == group:
		write(200, map[string]string{"location": "westus"})
	case r.Method == "GET" && r.URL.Path == vnet:
		write(200, map[string]string{"location": f.location})
	case r.Method == "GET" && r.URL.Path == subnet:
		write(200, map[string]any{"properties": map[string]any{"networkSecurityGroup": map[string]string{"id": nsg}}})
	case r.Method == "GET" && r.URL.Path == nsg:
		rule := func(name, direction, access, source string, sources ...string) map[string]any {
			return map[string]any{"name": name, "properties": map[string]any{"direction": direction, "access": access, "sourceAddressPrefix": source, "sourceAddressPrefixes": sources}}
		}
		write(200, map[string]any{"name": "open", "properties": map[string]any{"securityRules": []any{
			rule("ssh", "Inbound", "Allow", "Internet"),
			rule("office", "Inbound", "Allow", "", "203.0.113.0/24", "0.0.0.0/0"),
			rule("vnet", "Inbound", "Allow", "VirtualNetwork"),
			rule("deny", "Inbound", "Deny", "*"),
			rule("out", "Outbound", "Allow", "*"),
		}}})
	default:
		f.unexpects = append(f.unexpects, r.Method+" "+r.URL.String())
		armFail(400, "Unexpected")
	}
}

func testClient(t *testing.T, zones ...string) (*Client, *fakeARM) {
	f := &fakeARM{vms: map[string]map[string]any{}, power: map[string]string{}, refuse: map[string]string{}, fail: map[string]string{},
		failed: map[string]string{}, bodies: map[string]map[string]any{}, location: "East US"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	c := &Client{config: Config{Installation: "Inst-1", SubscriptionID: "sub", ResourceGroup: "rg", Region: "eastus", Zones: zones,
		Subnet: subnet, Architecture: "amd64", DiskGiB: 80}, http: srv.Client(), arm: srv.URL, poll: time.Millisecond, token: staticToken("tok")}
	t.Cleanup(func() {
		if len(f.unexpects) > 0 {
			t.Error("unexpected calls:", f.unexpects)
		}
	})
	return c, f
}

func TestLaunchFallsThroughAndBuildsSpotVM(t *testing.T) {
	c, f := testClient(t, "1", "2")
	ctx := context.Background()
	f.refuse["Standard_D4as_v5/1"] = "SkuNotAvailable"
	f.fail["Standard_D4as_v5/2"] = "ZonalAllocationFailed"
	tags := map[string]string{vm.TagCapacity: "azure", vm.TagPool: "app/svc", vm.TagSlot: "slot1"}
	i, err := c.Launch(ctx, vm.LaunchInput{Name: "Slot1", InstanceTypes: []string{"Standard_D4as_v5", "Standard_D4s_v5"}, Spot: true,
		UserData: "#!/bin/bash\necho job\n", Tags: tags})
	if err != nil || !strings.HasPrefix(i.ID, "rg/hakopod-slot1-") || i.Type != "Standard_D4s_v5" || i.Lifecycle != "spot" || i.State != "pending" {
		t.Fatal("launch did not move on to the next size", i, err)
	}
	if len(f.deleted) != 1 || f.vms[f.deleted[0]] != nil {
		t.Fatal("the VM whose allocation failed was not deleted", f.deleted)
	}
	body := f.bodies[strings.TrimPrefix(i.ID, "rg/")]
	encoded, _ := json.Marshal(body)
	for _, want := range []string{`"priority":"Spot"`, `"evictionPolicy":"Deallocate"`, `"maxPrice":-1`, `"zones":["1"]`,
		`"storageAccountType":"StandardSSD_LRS"`, `"diskSizeGB":80`, `"publisher":"Canonical"`, `"offer":"ubuntu-24_04-lts"`, `"sku":"server"`,
		`"disablePasswordAuthentication":true`, `"keyData":"ssh-rsa AAAAB3NzaC1yc2E`, `"id":"` + subnet + `"`, `"name":"Standard","tier":"Regional"`,
		`"bootDiagnostics":{"enabled":true}`, `"location":"eastus"`, `"hakopod-pool":"app/svc"`, `"hakopod-installation":"Inst-1"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatal("launch body lacks", want, string(encoded))
		}
	}
	if n := strings.Count(string(encoded), `"deleteOption":"Delete"`); n != 3 {
		t.Fatal("the disk, NIC and public IP must go with the VM", n)
	}
	if strings.Contains(string(encoded), "identity") || strings.Contains(string(encoded), "adminPassword") {
		t.Fatal("runner VM was given an identity or a password", string(encoded))
	}
	custom, _ := base64.StdEncoding.DecodeString(body["properties"].(map[string]any)["osProfile"].(map[string]any)["customData"].(string))
	if string(custom) != "#!/bin/bash\necho job\n" {
		t.Fatal("customData", string(custom))
	}

	// No zone and on-demand; every size refused is no capacity.
	c.config.Zones = nil
	f.refuse["Standard_B1s/"] = "OperationNotAllowed" // with a quota message below
	_, err = c.Launch(ctx, vm.LaunchInput{Name: "x", InstanceTypes: []string{"Standard_B1s"}})
	if err == nil || errors.Is(err, vm.ErrNoCapacity) {
		t.Fatal("a refusal that is not about quota or capacity must surface", err)
	}
	f.refuse["Standard_B1s/"] = "SpotVMQuotaExceeded"
	if _, err = c.Launch(ctx, vm.LaunchInput{Name: "x", InstanceTypes: []string{"Standard_B1s"}}); !errors.Is(err, vm.ErrNoCapacity) {
		t.Fatal(err)
	}
	delete(f.refuse, "Standard_B1s/")
	on, err := c.Launch(ctx, vm.LaunchInput{Name: "x", InstanceTypes: []string{"Standard_B1s"}, Image: "/subscriptions/sub/resourceGroups/img/providers/Microsoft.Compute/images/runner"})
	if err != nil || on.Lifecycle != "on-demand" {
		t.Fatal(on, err)
	}
	encoded, _ = json.Marshal(f.bodies[strings.TrimPrefix(on.ID, "rg/")])
	if !strings.Contains(string(encoded), `"priority":"Regular"`) || strings.Contains(string(encoded), "evictionPolicy") || strings.Contains(string(encoded), `"zones"`) ||
		!strings.Contains(string(encoded), `"imageReference":{"id":"/subscriptions/sub/resourceGroups/img/providers/Microsoft.Compute/images/runner"}`) {
		t.Fatal(string(encoded))
	}
	if _, err = c.Launch(ctx, vm.LaunchInput{Name: "x", InstanceTypes: []string{"Standard_B1s"}, Image: vm.ImageHakopodMinimal}); err == nil {
		t.Fatal("hakopod-minimal launched on Azure")
	}
}

func TestStateListTerminateAndCheck(t *testing.T) {
	c, f := testClient(t)
	ctx := context.Background()
	c.config.Architecture = "arm64"
	f.forbidOps = true // the create is then followed on the VM itself
	i, err := c.Launch(ctx, vm.LaunchInput{Name: "slot1", InstanceTypes: []string{"Standard_D4ps_v5"}, Spot: true, Tags: map[string]string{vm.TagSlot: "slot1"}})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(f.bodies[strings.TrimPrefix(i.ID, "rg/")]); !strings.Contains(string(b), `"sku":"server-arm64"`) {
		t.Fatal(string(b))
	}
	name := strings.TrimPrefix(i.ID, "rg/")
	for power, want := range map[string]string{"starting": "pending", "running": "running", "stopping": "stopping", "stopped": "stopped", "deallocated": "stopped"} {
		f.power[name] = power
		s, err := c.State(ctx, i.ID)
		if err != nil || s.State != want || s.Type != "Standard_D4ps_v5" || s.Lifecycle != "spot" || s.LaunchedAt.IsZero() {
			t.Fatal(power, s, err)
		}
		// Only an evicted Spot VM is deallocated; a runner powers off to stopped.
		if s.Interrupted() != (power == "deallocated") {
			t.Fatal(power, "interrupted", s.Interrupted())
		}
	}
	f.vms[name]["properties"].(map[string]any)["provisioningState"] = "Deleting"
	if s, _ := c.State(ctx, i.ID); s.State != "shutting-down" || !s.Gone() {
		t.Fatal(s)
	}
	f.vms[name]["properties"].(map[string]any)["provisioningState"] = "Succeeded"

	// Another installation's VM and an untagged one share the resource group.
	f.vms["other"] = map[string]any{"name": "other", "tags": map[string]string{vm.TagInstallation: "Inst-2", vm.TagSlot: "slot1"},
		"properties": map[string]any{"provisioningState": "Succeeded"}}
	f.vms["mine"] = map[string]any{"name": "mine", "tags": map[string]string{vm.TagInstallation: "Inst-1", vm.TagSlot: "slot2"},
		"properties": map[string]any{"provisioningState": "Succeeded"}}
	f.vms["bare"] = map[string]any{"name": "bare", "properties": map[string]any{"provisioningState": "Succeeded"}}
	listed, err := c.List(ctx, map[string]string{vm.TagSlot: "slot1"})
	if err != nil || len(listed) != 1 || listed[0].ID != i.ID || listed[0].Tags[vm.TagInstallation] != "Inst-1" {
		t.Fatal("listed", listed, err)
	}
	if all, _ := c.List(ctx, nil); len(all) != 2 {
		t.Fatal("this installation has two VMs", all)
	}
	for _, id := range []string{"rg/other", "rg/bare"} {
		if err = c.Terminate(ctx, id); err == nil || f.vms[strings.TrimPrefix(id, "rg/")] == nil {
			t.Fatal("terminated a VM without this installation's tag", id)
		}
	}
	if err = c.Terminate(ctx, "elsewhere/"+name); err == nil {
		t.Fatal("terminated a VM outside the resource group")
	}
	if err = c.Terminate(ctx, i.ID); err != nil || f.vms[name] != nil {
		t.Fatal(err)
	}
	if _, err = c.State(ctx, i.ID); !errors.Is(err, vm.ErrNotFound) {
		t.Fatal(err)
	}
	if err = c.Terminate(ctx, i.ID); err != nil {
		t.Fatal("terminating a deleted VM", err)
	}

	if out, err := c.ConsoleOutput(ctx, "rg/mine"); err != nil || !strings.Contains(out, "starting the runner") || f.blobAuth != "" {
		t.Fatal("console", out, err, f.blobAuth)
	}
	for _, unsupported := range []error{c.Claim(ctx, "rg/mine", "", nil), c.DeleteImage(ctx, "x")} {
		if !errors.Is(unsupported, errUnsupported) {
			t.Fatal(unsupported)
		}
	}

	warnings, err := c.Check(ctx)
	if err != nil || len(warnings) != 2 || !strings.Contains(warnings[0], "rule ssh") || !strings.Contains(warnings[1], "rule office") {
		t.Fatal(warnings, err)
	}
	f.location = "westeurope"
	if _, err = c.Check(ctx); err == nil || !strings.Contains(err.Error(), "westeurope") {
		t.Fatal("subnet in another region accepted", err)
	}
}

func TestImageSettings(t *testing.T) {
	c := &Client{config: Config{Architecture: "amd64"}}
	for setting, want := range map[string]string{
		"":             "Canonical:ubuntu-24_04-lts:server:latest",
		vm.ImageUbuntu: "Canonical:ubuntu-24_04-lts:server:latest",
		"Canonical:ubuntu-24_04-lts:server:24.04.202411030":                                                 "Canonical:ubuntu-24_04-lts:server:24.04.202411030",
		"/subscriptions/s/resourceGroups/g/providers/Microsoft.Compute/galleries/x/images/y/versions/1.0.0": "/subscriptions/s/resourceGroups/g/providers/Microsoft.Compute/galleries/x/images/y/versions/1.0.0",
	} {
		if got, err := c.Image(context.Background(), setting); err != nil || got != want {
			t.Fatal(setting, got, err)
		}
	}
	for _, bad := range []string{vm.ImageRunsOnFull, vm.ImageHakopodMinimal, "ubuntu", "a:b:c"} {
		if _, err := c.Image(context.Background(), bad); err == nil {
			t.Fatal(bad, "accepted")
		}
	}
}

func TestIdentitySources(t *testing.T) {
	var mu sync.Mutex
	seen := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = r.ParseForm()
		switch {
		case r.URL.Path == "/tenant/oauth2/v2.0/token" && r.Form.Get("grant_type") == "client_credentials" && r.Form.Get("scope") == scope &&
			r.Form.Get("client_id") == "app" && r.Form.Get("client_assertion") == "projected" &&
			r.Form.Get("client_assertion_type") == "urn:ietf:params:oauth:client-assertion-type:jwt-bearer":
			seen = append(seen, "federated")
			_, _ = w.Write([]byte(`{"access_token":"fed","expires_in":3600}`))
		case r.URL.Path == "/tenant/oauth2/v2.0/token" && r.Form.Get("grant_type") == "client_credentials" && r.Form.Get("scope") == scope &&
			r.Form.Get("client_id") == "app" && r.Form.Get("client_secret") == "s3cret":
			seen = append(seen, "secret")
			_, _ = w.Write([]byte(`{"access_token":"sec","expires_in":3600}`))
		case r.URL.Path == "/imds" && r.Header.Get("Metadata") == "true" && r.URL.Query().Get("resource") == resource &&
			r.URL.Query().Get("api-version") == "2018-02-01":
			seen = append(seen, "imds:"+r.URL.Query().Get("client_id"))
			_, _ = w.Write([]byte(`{"access_token":"mi","expires_in":"3600"}`))
		default:
			w.WriteHeader(400)
		}
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(file, []byte("projected\n"), 0o600)
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"AZURE_TENANT_ID": "tenant", "AZURE_CLIENT_ID": "app", "AZURE_FEDERATED_TOKEN_FILE": file, "AZURE_CLIENT_SECRET": "s3cret"}, "fed"},
		{map[string]string{"AZURE_TENANT_ID": "tenant", "AZURE_CLIENT_ID": "app", "AZURE_CLIENT_SECRET": "s3cret"}, "sec"},
		{map[string]string{"AZURE_CLIENT_ID": "user-assigned"}, "mi"},
		{map[string]string{}, "mi"},
	} {
		for _, k := range []string{"AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_FEDERATED_TOKEN_FILE", "AZURE_CLIENT_SECRET", "AZURE_AUTHORITY_HOST"} {
			t.Setenv(k, tc.env[k])
		}
		s := identity(srv.Client(), srv.URL, srv.URL+"/imds")
		for range 2 {
			if got, err := s.Token(context.Background()); err != nil || got != tc.want {
				t.Fatal(tc.env, got, err)
			}
		}
	}
	if strings.Join(seen, ",") != "federated,secret,imds:user-assigned,imds:" {
		t.Fatal("each source once, then cached:", seen)
	}
	t.Setenv("AZURE_CLIENT_ID", "")
	if _, err := identity(srv.Client(), srv.URL, srv.URL+"/nowhere").Token(context.Background()); err == nil || !strings.Contains(err.Error(), "managed identity") {
		t.Fatal(err)
	}
}
