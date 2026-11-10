package gcp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions/vm"
)

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

// fakeCompute keeps instances by zone/name and records each call.
type fakeCompute struct {
	mu        sync.Mutex
	instances map[string]map[string]any
	images    map[string]map[string]any
	exhausted map[string]bool // zones that refuse inserts
	preempted []string        // zone/name
	calls     []string
	bodies    map[string]map[string]any
}

func (f *fakeCompute) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(401)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/projects/proj/")
	call := r.Method + " " + path
	f.calls = append(f.calls, call)
	var body map[string]any
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
		f.bodies[call] = body
	}
	parts := strings.Split(path, "/")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	fail := func(code int, reason string) {
		w.WriteHeader(code)
		write(map[string]any{"error": map[string]any{"status": "X", "errors": []map[string]string{{"reason": reason}}}})
	}
	done := func(zone string) { write(map[string]any{"name": "op", "zone": zone, "status": "DONE"}) }
	switch {
	case r.Method == "POST" && len(parts) == 3 && parts[0] == "zones" && parts[2] == "instances":
		zone := parts[1]
		if f.exhausted[zone] {
			write(map[string]any{"name": "op", "zone": "zones/" + zone, "status": "DONE", "error": map[string]any{"errors": []map[string]string{{"code": "ZONE_RESOURCE_POOL_EXHAUSTED"}}}})
			return
		}
		body["zone"] = "https://compute/zones/" + zone
		body["status"] = "PROVISIONING"
		body["machineType"] = "https://compute/zones/" + zone + "/machineTypes/" + last(body["machineType"].(string))
		body["labelFingerprint"] = "lf"
		body["metadata"].(map[string]any)["fingerprint"] = "mf"
		f.instances[zone+"/"+body["name"].(string)] = body
		write(map[string]any{"name": "op", "zone": "zones/" + zone, "status": "RUNNING"})
	case r.Method == "POST" && parts[0] == "zones" && len(parts) == 5 && parts[2] == "operations" && parts[4] == "wait":
		done("zones/" + parts[1])
	case r.Method == "GET" && len(parts) == 4 && parts[0] == "zones" && parts[2] == "instances":
		i, ok := f.instances[parts[1]+"/"+parts[3]]
		if !ok {
			fail(404, "notFound")
			return
		}
		write(i)
	case r.Method == "DELETE" && len(parts) == 4 && parts[2] == "instances":
		delete(f.instances, parts[1]+"/"+parts[3])
		done("zones/" + parts[1])
	case r.Method == "POST" && len(parts) == 5 && parts[2] == "instances":
		i := f.instances[parts[1]+"/"+parts[3]]
		switch parts[4] {
		case "setLabels":
			if body["labelFingerprint"] != i["labelFingerprint"] {
				fail(412, "conditionNotMet")
				return
			}
			i["labels"] = body["labels"]
		case "setMetadata":
			if body["fingerprint"] != i["metadata"].(map[string]any)["fingerprint"] {
				fail(412, "conditionNotMet")
				return
			}
			i["metadata"] = map[string]any{"items": body["items"], "fingerprint": "mf2"}
		case "start":
			i["status"] = "RUNNING"
		}
		done("zones/" + parts[1])
	case r.Method == "GET" && len(parts) == 5 && parts[4] == "serialPort":
		write(map[string]string{"contents": "hakopod: image ready\n"})
	case r.Method == "GET" && path == "aggregated/instances":
		items := map[string]any{}
		for key, i := range f.instances {
			zone, _, _ := strings.Cut(key, "/")
			scoped, _ := items["zones/"+zone].(map[string]any)
			if scoped == nil {
				scoped = map[string]any{"instances": []any{}}
			}
			// The fake ignores the filter: the client must check exact tags.
			scoped["instances"] = append(scoped["instances"].([]any), i)
			items["zones/"+zone] = scoped
		}
		f.bodies["filter"] = map[string]any{"filter": r.URL.Query().Get("filter")}
		write(map[string]any{"items": items})
	case r.Method == "GET" && len(parts) == 3 && parts[2] == "operations":
		ops := []map[string]string{}
		for _, p := range f.preempted {
			if zone, name, _ := strings.Cut(p, "/"); zone == parts[1] {
				ops = append(ops, map[string]string{"targetLink": "https://compute/zones/" + zone + "/instances/" + name})
			}
		}
		write(map[string]any{"items": ops})
	case r.Method == "POST" && path == "global/images":
		body["status"] = "PENDING"
		f.images[body["name"].(string)] = body
		write(map[string]any{"name": "op", "status": "RUNNING"})
	case r.Method == "GET" && path == "global/images":
		list := []any{}
		for _, i := range f.images {
			list = append(list, i)
		}
		write(map[string]any{"items": list})
	case r.Method == "GET" && strings.HasPrefix(path, "global/images/"):
		i, ok := f.images[last(path)]
		if !ok {
			fail(404, "notFound")
			return
		}
		write(i)
	case r.Method == "DELETE" && strings.HasPrefix(path, "global/images/"):
		delete(f.images, last(path))
		write(map[string]any{"name": "op", "status": "RUNNING"})
	case r.Method == "GET" && r.URL.Path == "/projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-arm64":
		write(map[string]string{"name": "ubuntu-2404-noble-arm64-v20261101"})
	case r.Method == "GET" && path == "regions/us-central1/subnetworks/runners":
		write(map[string]string{"network": "https://compute/global/networks/default"})
	case r.Method == "GET" && path == "global/firewalls":
		write(map[string]any{"items": []map[string]any{
			{"name": "default-allow-ssh", "network": "https://compute/global/networks/default", "direction": "INGRESS", "sourceRanges": []string{"0.0.0.0/0"}, "allowed": []any{map[string]any{"IPProtocol": "tcp"}}},
			{"name": "tagged", "network": "https://compute/global/networks/default", "direction": "INGRESS", "sourceRanges": []string{"0.0.0.0/0"}, "targetTags": []string{"web"}, "allowed": []any{1}},
			{"name": "elsewhere", "network": "https://compute/global/networks/other", "direction": "INGRESS", "sourceRanges": []string{"0.0.0.0/0"}, "allowed": []any{1}},
		}})
	default:
		fail(400, "unexpected "+call)
	}
}

func testClient(t *testing.T) (*Client, *fakeCompute) {
	f := &fakeCompute{instances: map[string]map[string]any{}, images: map[string]map[string]any{}, exhausted: map[string]bool{}, bodies: map[string]map[string]any{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := &Client{config: Config{Installation: "Inst-1", Project: "proj", Region: "us-central1", Zones: []string{"us-central1-a", "us-central1-b"},
		Subnetwork: "runners", ServiceAccount: "sa@proj.iam.gserviceaccount.com", Architecture: "arm64", DiskGiB: 80},
		http: srv.Client(), compute: srv.URL, token: staticToken("tok")}
	return c, f
}

func TestLaunchListAndPreemption(t *testing.T) {
	c, f := testClient(t)
	ctx := context.Background()
	f.exhausted["us-central1-a"] = true
	tags := map[string]string{vm.TagCapacity: "gcp", vm.TagPool: "app/svc", vm.TagSlot: "slot1"}
	i, err := c.Launch(ctx, vm.LaunchInput{Name: "slot1", InstanceTypes: []string{"c4a-standard-2"}, Spot: true, UserData: "#!/bin/bash\necho job\n", Tags: tags})
	if err != nil || !strings.HasPrefix(i.ID, "us-central1-b/hakopod-slot1-") || i.Lifecycle != "spot" || i.Type != "c4a-standard-2" {
		t.Fatal("launch did not move on to the next zone", i, err)
	}
	body := f.bodies["POST zones/us-central1-b/instances"]
	encoded, _ := json.Marshal(body)
	for _, want := range []string{`"provisioningModel":"SPOT"`, `"instanceTerminationAction":"STOP"`, `hyperdisk-balanced`, `"diskSizeGb":"80"`,
		`family/ubuntu-2404-lts-arm64`, `"block-project-ssh-keys"`, `subnetworks/runners`, `"hakopod-slot":"slot1"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatal("launch body lacks", want, string(encoded))
		}
	}
	if strings.Contains(string(encoded), "serviceAccounts") {
		t.Fatal("runner VM was given a Google credential")
	}
	// The pool value and installation are not valid label values; they are
	// labelled by hash and kept exactly in metadata.
	got := body["labels"].(map[string]any)
	if got[vm.TagPool] != label("app/svc") || got[vm.TagInstallation] != label("Inst-1") || !strings.HasPrefix(label("app/svc"), "h") {
		t.Fatal("labels", got)
	}
	// Another installation's instance with the same labels is never listed.
	f.instances["us-central1-a/other"] = map[string]any{"name": "other", "zone": "https://compute/zones/us-central1-a", "status": "RUNNING",
		"labels": got, "metadata": map[string]any{"items": []map[string]string{{"key": tagsKey, "value": `{"hakopod-installation":"Inst-2","hakopod-slot":"slot1"}`}}}}
	listed, err := c.List(ctx, map[string]string{vm.TagSlot: "slot1"})
	if err != nil || len(listed) != 1 || listed[0].ID != i.ID || listed[0].Tags[vm.TagPool] != "app/svc" || listed[0].State != "pending" || listed[0].Interrupted() {
		t.Fatal("listed", listed, err)
	}
	if filter := f.bodies["filter"]["filter"].(string); !strings.Contains(filter, `(labels.hakopod-slot = "slot1")`) || !strings.Contains(filter, "labels.hakopod-installation") {
		t.Fatal("filter", filter)
	}
	// A reclaimed spot VM is stopped, and its zone records the preemption.
	f.instances[i.ID]["status"] = "TERMINATED"
	f.preempted = []string{i.ID}
	if s, err := c.State(ctx, i.ID); err != nil || !s.Interrupted() || !s.Gone() {
		t.Fatal("preemption not seen", s, err)
	}
	if err = c.Terminate(ctx, "us-central1-a/other"); err == nil {
		t.Fatal("terminated another installation's instance")
	}
	if err = c.Terminate(ctx, "europe-west1-b/x"); err == nil {
		t.Fatal("terminated an instance outside the capacity's zones")
	}
	if err = c.Terminate(ctx, i.ID); err != nil || f.instances[i.ID] != nil {
		t.Fatal(err)
	}
	if _, err = c.State(ctx, i.ID); !errors.Is(err, vm.ErrNotFound) {
		t.Fatal(err)
	}
	if err = c.Terminate(ctx, i.ID); err != nil {
		t.Fatal("terminating a deleted instance", err)
	}
	f.exhausted["us-central1-b"] = true
	if _, err = c.Launch(ctx, vm.LaunchInput{Name: "slot2", InstanceTypes: []string{"t2a-standard-1"}, Spot: true}); !errors.Is(err, vm.ErrNoCapacity) {
		t.Fatal("no capacity", err)
	}
}

func TestClaimImagesAndCheck(t *testing.T) {
	c, f := testClient(t)
	ctx := context.Background()
	standby, err := c.Launch(ctx, vm.LaunchInput{Name: "standby-1", InstanceTypes: []string{"t2a-standard-1"}, UserData: "warm", StopOnShutdown: true,
		Tags: map[string]string{vm.TagCapacity: "gcp", vm.TagPoolState: "standby", vm.TagImage: "projects/proj/global/images/img"}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(f.bodies["POST zones/us-central1-a/instances"])
	if !strings.Contains(string(encoded), `"provisioningModel":"STANDARD"`) || !strings.Contains(string(encoded), "pd-balanced") {
		t.Fatal(string(encoded))
	}
	if err = c.Claim(ctx, standby.ID, "job", map[string]string{vm.TagPoolState: "claimed", vm.TagSlot: "slot9"}); err == nil {
		t.Fatal("claimed a standby that is not stopped")
	}
	f.instances[standby.ID]["status"] = "TERMINATED"
	if err = c.Claim(ctx, standby.ID, "job", map[string]string{vm.TagPoolState: "claimed", vm.TagSlot: "slot9"}); err != nil {
		t.Fatal(err)
	}
	s, err := c.State(ctx, standby.ID)
	if err != nil || s.State != "running" || s.Tags[vm.TagSlot] != "slot9" || s.Tags[vm.TagPoolState] != "claimed" || s.Tags[vm.TagCapacity] != "gcp" {
		t.Fatal("claim", s, err)
	}
	items, _ := json.Marshal(f.instances[standby.ID]["metadata"])
	if !strings.Contains(string(items), `"value":"job"`) || !strings.Contains(string(items), "block-project-ssh-keys") || strings.Contains(string(items), "warm") {
		t.Fatal("claim metadata", string(items))
	}
	if out, err := c.ConsoleOutput(ctx, standby.ID); err != nil || !strings.Contains(out, vm.ImageReady) {
		t.Fatal(out, err)
	}

	resolved, err := c.Image(ctx, "projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-arm64")
	if err != nil || resolved != "projects/ubuntu-os-cloud/global/images/ubuntu-2404-noble-arm64-v20261101" {
		t.Fatal(resolved, err)
	}
	if _, err = c.Image(ctx, vm.ImageRunsOnFull); err == nil {
		t.Fatal("runs-on-full has no Compute Engine image")
	}
	id, err := c.CreateImage(ctx, standby.ID, "hakopod-minimal-GCP-20261110-101010", map[string]string{vm.TagCapacity: "gcp"})
	if err != nil || id != "projects/proj/global/images/hakopod-minimal-gcp-20261110-101010" {
		t.Fatal(id, err)
	}
	img := f.bodies["POST global/images"]
	if !strings.HasSuffix(img["sourceDisk"].(string), "/zones/us-central1-a/disks/"+strings.SplitN(standby.ID, "/", 2)[1]) || img["architecture"] != "ARM64" {
		t.Fatal(img)
	}
	f.images["foreign"] = map[string]any{"name": "foreign", "status": "READY", "labels": map[string]any{vm.TagInstallation: label("Inst-1")}, "description": `{"hakopod-installation":"Inst-2"}`}
	images, err := c.Images(ctx, map[string]string{vm.TagCapacity: "gcp"})
	if err != nil || len(images) != 1 || images[0].ID != id || images[0].State != "pending" {
		t.Fatal(images, err)
	}
	if err = c.DeleteImage(ctx, "projects/proj/global/images/foreign"); err == nil {
		t.Fatal("deleted another installation's image")
	}
	if err = c.DeleteImage(ctx, "projects/other/global/images/x"); err == nil {
		t.Fatal("deleted an image in another project")
	}
	if err = c.DeleteImage(ctx, id); err != nil || f.images["hakopod-minimal-gcp-20261110-101010"] != nil {
		t.Fatal(err)
	}
	warnings, err := c.Check(ctx)
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "default-allow-ssh") {
		t.Fatal(warnings, err)
	}
}

func TestServiceAccountJWTAndImpersonation(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	jwt, err := signJWT(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "me@p.iam.gserviceaccount.com", "https://oauth2.googleapis.com/token", time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		t.Fatal("bad signature")
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if !strings.Contains(string(claims), `"iss":"me@p.iam.gserviceaccount.com"`) || !strings.Contains(string(claims), `"exp":4600`) {
		t.Fatal(string(claims))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer own" || r.URL.Path != "/v1/projects/-/serviceAccounts/sa@proj.iam.gserviceaccount.com:generateAccessToken" {
			w.WriteHeader(403)
			return
		}
		_, _ = w.Write([]byte(`{"accessToken":"theirs","expireTime":"2099-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()
	calls := 0
	own := &tokenSource{fetch: func(context.Context) (token, error) { calls++; return token{"own", time.Now().Add(time.Hour)}, nil }}
	s := impersonate(srv.Client(), own, srv.URL, "sa@proj.iam.gserviceaccount.com")
	for range 2 {
		if got, err := s.Token(context.Background()); err != nil || got != "theirs" {
			t.Fatal(got, err)
		}
	}
	if calls != 1 {
		t.Fatal("impersonated token not cached", calls)
	}
	refused := impersonate(srv.Client(), &tokenSource{fetch: func(context.Context) (token, error) { return token{"stranger", time.Now().Add(time.Hour)}, nil }}, srv.URL, "sa@proj.iam.gserviceaccount.com")
	if _, err := refused.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "Token Creator") {
		t.Fatal(err)
	}
}
