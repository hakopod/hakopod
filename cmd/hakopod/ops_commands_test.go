package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestExportPlansBackToTheSameSpecification(t *testing.T) {
	app, err := spec.Parse([]byte(`schema_version = 1
name = "kole"

[services.kole]
image = "ghcr.io/syne/kole@sha256:` + strings.Repeat("a", 64) + `"
port = 8000
public = true

[services.kole.env]
KOLE_APP_API_URL = "http://app:3001/api"

[services.kole.secrets.ANTHROPIC_API_KEY]
ref = "anthropic-key"
`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exportTOML(store.Application{Name: "kole", Project: "demo", Environment: "production", Revision: 12, Spec: app})
	if err != nil {
		t.Fatal(err)
	}
	back, err := spec.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if changes := spec.Diff(&app, back); len(changes) != 0 {
		t.Fatalf("export does not round-trip: %+v\n%s", changes, out)
	}
	if !strings.HasPrefix(string(out), "# Exported from demo/production application kole at revision 12.") {
		t.Fatal(string(out))
	}
}

func TestSecretValueComesOnlyFromFileOrStdin(t *testing.T) {
	if _, err := readSecretValue("", false, nil); err == nil {
		t.Fatal("no source accepted")
	}
	if _, err := readSecretValue("x", true, nil); err == nil {
		t.Fatal("two sources accepted")
	}
	v, err := readSecretValue("", true, strings.NewReader("sk-live-123\n"))
	if err != nil || v != "sk-live-123" {
		t.Fatal(v, err)
	}
	if _, err := readSecretValue("", true, strings.NewReader("\n")); err == nil {
		t.Fatal("empty value accepted")
	}
}

func TestRestartSetImageAndExecPodSelection(t *testing.T) {
	var restartBody, planBody map[string]any
	var restartKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/applications/app/services/kole/restart":
			restartKey = r.Header.Get("Idempotency-Key")
			json.NewDecoder(r.Body).Decode(&restartBody)
			io.WriteString(w, `{"id":"d1","revision":13,"status":"queued"}`)
		case r.URL.Path == "/api/v1/plan":
			json.NewDecoder(r.Body).Decode(&planBody)
			io.WriteString(w, `{"expected_revision":12,"spec":{"name":"kole","services":{"kole":{"image":"ghcr.io/syne/kole:2"}}},"changes":[{"service":"kole","field":"image","before":"ghcr.io/syne/kole:1","after":"ghcr.io/syne/kole:2"}]}`)
		case r.URL.Path == "/api/v1/deployments":
			io.WriteString(w, `{"id":"d2","revision":13,"status":"queued"}`)
		case r.URL.Path == "/api/v1/applications/app/services/kole/runtime":
			io.WriteString(w, `{"pods":[{"name":"kole-7f-abc","ready":true},{"name":"kole-6c-old","ready":false}]}`)
		default:
			t.Fatal(r.URL.Path)
		}
	}))
	defer server.Close()
	c := &client{url: server.URL, key: "k", http: server.Client()}
	cfg := config{Project: "demo", Environment: "production"}
	a := store.Application{ID: "app", Name: "kole", Revision: 12, Spec: spec.Application{Name: "kole", Services: map[string]spec.Service{"kole": {Image: "ghcr.io/syne/kole:1"}}}}
	if _, err := restartService(context.Background(), c, a, "kole", "retry-key-123"); err != nil {
		t.Fatal(err)
	}
	if restartBody["expected_revision"] != float64(12) || restartKey != "retry-key-123" {
		t.Fatal(restartBody, restartKey)
	}
	if _, err := setImage(context.Background(), c, cfg, a, "kole", "ghcr.io/syne/kole:2", "retry-key-456", true); err != nil {
		t.Fatal(err)
	}
	if planBody["spec"].(map[string]any)["services"].(map[string]any)["kole"].(map[string]any)["image"] != "ghcr.io/syne/kole:2" {
		t.Fatal(planBody)
	}
	if a.Spec.Services["kole"].Image != "ghcr.io/syne/kole:1" {
		t.Fatal("set-image changed the caller's application")
	}
	if _, err := setImage(context.Background(), c, cfg, a, "kole", "ghcr.io/syne/kole:1", "retry-key-789", true); err == nil {
		t.Fatal("no-op image change accepted")
	}
	if pod, err := readyPod(context.Background(), c, a, "kole"); err != nil || pod != "kole-7f-abc" {
		t.Fatal(pod, err)
	}
	if svc, err := soleService(a, ""); err != nil || svc != "kole" {
		t.Fatal(svc, err)
	}
}

func TestManifestsRequestPath(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("manifests must only read: %s", r.Method)
		}
		paths = append(paths, r.URL.RequestURI())
		io.WriteString(w, `{"revision":3,"manifests":"apiVersion: apps/v1\nkind: Deployment\n"}`)
	}))
	defer server.Close()
	c := &client{url: server.URL, key: "k", http: server.Client()}
	a := store.Application{ID: "app", Name: "shop", Spec: spec.Application{Name: "shop", Services: map[string]spec.Service{"api": {Image: "nginx"}, "web": {Image: "nginx"}}}}
	out, err := renderManifests(context.Background(), c, a, "api")
	if err != nil || out != "apiVersion: apps/v1\nkind: Deployment\n" {
		t.Fatal(out, err)
	}
	if _, err := renderManifests(context.Background(), c, a, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := renderManifests(context.Background(), c, a, "missing"); err == nil {
		t.Fatal("unknown service accepted")
	}
	if len(paths) != 2 || paths[0] != "/api/v1/applications/app/manifests?service=api" || paths[1] != "/api/v1/applications/app/manifests" {
		t.Fatal(paths)
	}
}

func TestHostnamesRequestPaths(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/applications":
			io.WriteString(w, `{"items":[{"id":"app","name":"shop"}]}`)
		case r.URL.Path == "/api/v1/applications/app":
			io.WriteString(w, `{"id":"app","name":"shop","spec":{"name":"shop","services":{"web":{"image":"nginx"}}}}`)
		default:
			body, _ := io.ReadAll(r.Body)
			calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+string(body))
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/custom-hostnames") {
				if r.URL.Query().Get("cursor") == "" {
					io.WriteString(w, `{"items":[{"hostname":"a.customer.com","status":"active"}],"next_cursor":"a.customer.com"}`)
				} else {
					io.WriteString(w, `{"items":[{"hostname":"b.customer.com","status":"pending_dns"}],"next_cursor":""}`)
				}
				return
			}
			io.WriteString(w, `{"hostname":"shop.customer.com","status":"pending_dns"}`)
		}
	}))
	defer server.Close()
	c := &client{url: server.URL, key: "k", http: server.Client()}
	cfg := config{Project: "demo", Environment: "production"}
	ctx := context.Background()
	for _, args := range [][]string{{"list", "shop"}, {"add", "shop", "shop.customer.com"}, {"show", "shop", "shop.customer.com"}, {"remove", "shop", "shop.customer.com"}} {
		status := ""
		if args[0] == "list" {
			status = "active"
		}
		if err := hostnamesCommand(ctx, c, cfg, args, "", "", status, true); err != nil {
			t.Fatal(args, err)
		}
	}
	base := "/api/v1/applications/app/services/web/custom-hostnames"
	want := []string{
		"GET " + base + "?limit=500&status=active ",
		"GET " + base + "?cursor=a.customer.com&limit=500&status=active ",
		"POST " + base + ` {"hostname":"shop.customer.com"}`,
		"GET " + base + "/shop.customer.com ",
		"DELETE " + base + "/shop.customer.com ",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
	if err := hostnamesCommand(ctx, c, cfg, []string{"add", "shop"}, "", "", "", false); err == nil {
		t.Fatal("add without a hostname accepted")
	}
}

func TestActionsCapacityCommand(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(body))
		if r.URL.Path == "/api/v1/settings/actions-capacity" {
			io.WriteString(w, `{"external_id":"hakopod-x","capacities":[],"revision":4}`)
			return
		}
		io.WriteString(w, `{"capacity":"aws-spot","warnings":[]}`)
	}))
	defer server.Close()
	c := &client{url: server.URL, key: "k", http: server.Client()}
	file := t.TempDir() + "/capacity.toml"
	os.WriteFile(file, []byte(`[[capacity]]
name = "aws-spot"
cloud = "aws"
region = "eu-central-1"
role_arn = "arn:aws:iam::123456789012:role/hakopod-actions"
subnet_ids = ["subnet-0abc1234"]
security_group_ids = ["sg-0abc1234"]
instance_types = ["c7i.xlarge"]
architecture = "amd64"
spot = true
max_instances = 20
environments = ["shop/production"]
image = "hakopod-minimal"
warm_pool = 2
`), 0o600)
	ctx := context.Background()
	if err := actionsCapacityCommand(ctx, c, []string{"capacity", "set", file}, false, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := actionsCapacityCommand(ctx, c, []string{"capacity", "test", "aws-spot"}, true, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := actionsCapacityCommand(ctx, c, []string{"capacity", "build-image", "aws-spot"}, false, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || !strings.HasPrefix(calls[1], `PUT /api/v1/settings/actions-capacity {"capacities":[{"name":"aws-spot",`) || !strings.Contains(calls[1], `"expected_revision":4`) ||
		!strings.Contains(calls[1], `"image":"hakopod-minimal","warm_pool":2`) ||
		calls[2] != `POST /api/v1/settings/actions-capacity/aws-spot/test {"launch":true}` || calls[3] != `POST /api/v1/settings/actions-capacity/aws-spot/build-image {}` {
		t.Fatal(calls)
	}
	if actionsCapacityCommand(ctx, c, []string{"capacity", "test"}, false, false, io.Discard) == nil {
		t.Fatal("usage accepted")
	}
}
