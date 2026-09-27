package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

// treeServer fakes the API for a tree with an unchanged network. planFails names the
// application whose /plan is rejected; deployStatus is returned for every deployment.
// planFails "network" makes every /virtual-networks request forbidden. Writes are
// recorded in the first slice, every /virtual-networks request in the second.
func treeServer(t *testing.T, planFails, deployStatus string) (*client, *[]string, *[]string) {
	t.Helper()
	var mu sync.Mutex
	writes, networkCalls := []string{}, []string{}
	network, _ := spec.ParseVirtualNetwork([]byte(treeNetwork))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/v1/virtual-networks") {
			mu.Lock()
			networkCalls = append(networkCalls, r.Method+" "+r.URL.Path)
			mu.Unlock()
			if planFails == "network" {
				w.WriteHeader(403)
				w.Write([]byte(`{"error":{"code":"forbidden","message":"virtual network management requires an administrator"}}`))
				return
			}
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/virtual-networks/plan":
			json.NewEncoder(w).Encode(networkPlan{Spec: network, Previous: &network, ExpectedRevision: 1})
		case "POST /api/v1/plan":
			var in struct {
				TOML string
				Spec spec.Application
			}
			json.Unmarshal(body, &in)
			app := in.Spec // a merged folder sends the application as JSON instead of TOML
			if in.TOML != "" {
				app, _ = spec.Parse([]byte(in.TOML))
			}
			if app.Name == planFails {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":{"code":"invalid","message":"segment not granted"}}`))
				return
			}
			plan := appPlan{Spec: app}
			if planFails == "unchanged" {
				plan.ApplicationID = "app-" + app.Name
			}
			json.NewEncoder(w).Encode(plan)
		case "GET /api/v1/applications/app-alpha", "GET /api/v1/applications/app-beta":
			// alpha is healthy; beta's last release failed, so it must be resubmitted.
			status := map[bool]string{true: "healthy", false: "failed"}[strings.HasSuffix(r.URL.Path, "alpha")]
			w.Write([]byte(`{"id":"x","status":"` + status + `"}`))
		default:
			mu.Lock()
			writes = append(writes, r.Method+" "+r.URL.Path+" "+r.Header.Get("Idempotency-Key"))
			mu.Unlock()
			w.Write([]byte(`{"id":"d` + string(rune('0'+len(writes))) + `","status":"` + deployStatus + `"}`))
		}
	}))
	t.Cleanup(server.Close)
	return &client{url: server.URL, key: "k", http: server.Client()}, &writes, &networkCalls
}

func twoAppTree(t *testing.T) string {
	return writeTree(t, map[string]string{
		"network.toml":       treeNetwork,
		"alpha/HAKOPOD.toml": treeAppTOML("alpha", "telemetry"),
		"beta/hakopod.toml":  treeAppTOML("beta", "telemetry"),
		"notes/readme.txt":   "not an application",
	})
}

var treeCfg = config{Project: "demo", Environment: "development"}

func TestTreePlansEverythingBeforeAnyWrite(t *testing.T) {
	c, writes, _ := treeServer(t, "beta", "queued")
	err := treeCommand(context.Background(), c, treeCfg, "deploy", twoAppTree(t), nil, false, false, true)
	if err == nil || exitCode(err) != 2 {
		t.Fatalf("expected input failure, got %v", err)
	}
	if len(*writes) != 0 {
		t.Fatalf("writes before all plans succeeded: %v", *writes)
	}
}

func TestTreeStopsAtFirstFailedRelease(t *testing.T) {
	c, writes, _ := treeServer(t, "", "failed")
	err := treeCommand(context.Background(), c, treeCfg, "deploy", twoAppTree(t), nil, false, true, true)
	if exitCode(err) != 5 {
		t.Fatalf("expected exit 5, got %v", err)
	}
	if len(*writes) != 1 || !strings.HasPrefix((*writes)[0], "POST /api/v1/deployments ") {
		t.Fatalf("expected exactly one deployment, got %v", *writes)
	}
}

func TestTreeDeploysEachAppWithOwnKey(t *testing.T) {
	c, writes, _ := treeServer(t, "", "queued")
	if err := treeCommand(context.Background(), c, treeCfg, "deploy", twoAppTree(t), nil, false, false, true); err != nil {
		t.Fatal(err)
	}
	if len(*writes) != 2 || (*writes)[0] == (*writes)[1] {
		t.Fatalf("expected two deployments with distinct keys, got %v", *writes)
	}
	// --only narrows the tree and a plan run makes no writes.
	*writes = nil
	if err := treeCommand(context.Background(), c, treeCfg, "plan", twoAppTree(t), []string{"beta"}, false, false, true); err != nil || len(*writes) != 0 {
		t.Fatal(err, *writes)
	}
}

// TestTreePlanBodyPerPath is the server contract: exactly one of spec or toml per request.
// A merged folder has no single document to send, a legacy folder still sends its TOML.
func TestTreePlanBodyPerPath(t *testing.T) {
	root := writeTree(t, map[string]string{
		"legacy/hakopod.toml": "name = 'legacy'\nenv_file = '.env'\n[services.web]\nimage = 'nginx'\n",
		"legacy/.env":         "TOKEN=abc\n",
		"merged/web.toml":     "image = 'nginx'\n",
		"merged/worker.toml":  "image = 'busybox'\n",
	})
	var mu sync.Mutex
	bodies := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var in map[string]any
		json.Unmarshal(raw, &in)
		var app spec.Application
		kind := "merged"
		if _, sendsTOML := in["toml"]; sendsTOML {
			kind = "legacy"
		} else if encoded, err := json.Marshal(in["spec"]); err == nil {
			json.Unmarshal(encoded, &app)
		}
		mu.Lock()
		bodies[kind] = in
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(appPlan{Spec: app})
	}))
	defer server.Close()
	c := &client{url: server.URL, key: "k", http: server.Client()}
	if err := treeCommand(context.Background(), c, treeCfg, "plan", root, nil, false, false, true); err != nil {
		t.Fatal(err)
	}

	merged, legacy := bodies["merged"], bodies["legacy"]
	if merged == nil || legacy == nil {
		t.Fatalf("both applications must be planned, got %v", bodies)
	}
	if _, ok := merged["toml"]; ok {
		t.Fatalf("merged folder sent toml: %v", merged)
	}
	services, _ := merged["spec"].(map[string]any)["services"].(map[string]any)
	if len(services) != 2 {
		t.Fatalf("merged folder must send the merged spec, got %v", merged["spec"])
	}
	if _, ok := legacy["spec"]; ok {
		t.Fatalf("legacy folder sent spec: %v", legacy)
	}
	if legacy["toml"] == "" || legacy["env_files"].(map[string]any)[".env"] != "TOKEN=abc\n" {
		t.Fatalf("legacy folder must send toml plus env_files, got %v", legacy)
	}
}

func TestTreeDeploysMergedFolder(t *testing.T) {
	c, writes, _ := treeServer(t, "", "queued")
	root := writeTree(t, map[string]string{"merged/web.toml": "image = 'nginx'\n", "merged/worker.toml": "image = 'busybox'\n"})
	if err := treeCommand(context.Background(), c, treeCfg, "deploy", root, nil, false, false, true); err != nil {
		t.Fatal(err)
	}
	if len(*writes) != 1 || !strings.HasPrefix((*writes)[0], "POST /api/v1/deployments ") {
		t.Fatalf("expected one deployment for the merged folder, got %v", *writes)
	}
}

func TestDirRejectsSingleAppFlags(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()
	for _, args := range [][]string{
		{"deploy", "--dir", ".", "--idempotency-key", "k"},
		{"deploy", "--dir", ".", "--service", "web"},
		{"plan", "--only", "a"},
		{"status", "--dir", "."},
		{"deploy", "--dir", ".", "--file", "hakopod.toml"},
		{"deploy", "--no-network"},
	} {
		os.Args = append([]string{"hakopod"}, args...)
		var e *exitError
		if err := run(); !errors.As(err, &e) || e.code != 2 {
			t.Errorf("%v: expected exit 2, got %v", args, err)
		}
	}
}

func TestTreeNoNetworkSkipsNetworkAPI(t *testing.T) {
	c, writes, networkCalls := treeServer(t, "", "queued")
	if err := treeCommand(context.Background(), c, treeCfg, "deploy", twoAppTree(t), nil, true, false, true); err != nil {
		t.Fatal(err)
	}
	if len(*networkCalls) != 0 || len(*writes) != 2 {
		t.Fatalf("network calls %v, writes %v", *networkCalls, *writes)
	}
}

func TestTreeForbiddenNetworkExplainsNoNetwork(t *testing.T) {
	c, writes, _ := treeServer(t, "network", "queued")
	err := treeCommand(context.Background(), c, treeCfg, "deploy", twoAppTree(t), nil, false, false, true)
	if exitCode(err) != 3 || !strings.Contains(err.Error(), "forbidden:") || !strings.Contains(err.Error(), "rerun with --no-network") {
		t.Fatalf("got %v", err)
	}
	if len(*writes) != 0 {
		t.Fatalf("writes after network failure: %v", *writes)
	}
}

func TestTreeSkipsOnlyHealthyUnchangedApps(t *testing.T) {
	c, writes, _ := treeServer(t, "unchanged", "queued")
	if err := treeCommand(context.Background(), c, treeCfg, "deploy", twoAppTree(t), nil, true, false, true); err != nil {
		t.Fatal(err)
	}
	if len(*writes) != 1 || !strings.HasPrefix((*writes)[0], "POST /api/v1/deployments ") {
		t.Fatalf("expected only the failed app to be resubmitted, got %v", *writes)
	}
}
