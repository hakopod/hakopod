package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func TestHostnameCreateRateLimitWindow(t *testing.T) {
	s := &Server{}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	for i := 0; i < hostnameCreatesPerMinute; i++ {
		if ok, _ := s.allowHostnameCreate("app", now.Add(time.Duration(i)*time.Millisecond)); !ok {
			t.Fatalf("create %d refused inside the budget", i+1)
		}
	}
	ok, wait := s.allowHostnameCreate("app", now.Add(20*time.Second))
	if ok || wait != 40*time.Second {
		t.Fatalf("create 121 within the minute: allowed=%v wait=%s", ok, wait)
	}
	if ok, _ := s.allowHostnameCreate("other", now.Add(20*time.Second)); !ok {
		t.Fatal("the limit is per application")
	}
	if ok, _ := s.allowHostnameCreate("app", now.Add(time.Minute)); !ok {
		t.Fatal("a new window must allow creates again")
	}
}

func TestCustomHostnameAPI(t *testing.T) {
	db := notificationTestDB(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	next, err := spec.Parse([]byte("schema_version=1\nname='saas'\n[services.web]\nimage='nginx:alpine'\nport=8080\npublic=true\n[services.web.custom_hostnames]\nenabled=true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Accept(ctx, owner, "demo", "development", next, 0, "seed-saas"); err != nil {
		t.Fatal(err)
	}
	app, err := db.FindApplication(ctx, "demo", "development", "saas")
	if err != nil {
		t.Fatal(err)
	}
	key := func(permissions ...string) string {
		name := strings.Join(permissions, "+")
		_, k, err := db.CreateKey(ctx, owner, store.KeyInput{Name: name, Project: "demo", Environment: "development", Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	writer := key("deployments:read", "deployments:write")
	manager := key("deployments:read", "hostnames:manage")
	reader := key("deployments:read")
	srv := &Server{Store: db}
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)
	call := func(method, path, key string, body any) (int, http.Header, map[string]any) {
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, server.URL+"/api/v1/applications/"+app.ID+"/services/web/custom-hostnames"+path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return res.StatusCode, res.Header, out
	}
	if status, _, _ := call("POST", "", writer, map[string]string{"hostname": "shop.customer.com"}); status != 403 {
		t.Fatalf("deployments:write must not attach hostnames: %d", status)
	}
	if status, _, _ := call("POST", "", reader, map[string]string{"hostname": "shop.customer.com"}); status != 403 {
		t.Fatalf("readers must not attach hostnames: %d", status)
	}
	status, _, out := call("POST", "", manager, map[string]string{"hostname": "Shop.Customer.com"})
	if status != 201 || out["hostname"] != "shop.customer.com" || out["status"] != "pending_dns" {
		t.Fatalf("create: %d %v", status, out)
	}
	if status, _, _ = call("POST", "", manager, map[string]string{"hostname": "shop.customer.com"}); status != 200 {
		t.Fatalf("repeat create must be idempotent: %d", status)
	}
	if status, _, _ = call("POST", "", manager, map[string]string{"hostname": "*.customer.com"}); status != 400 {
		t.Fatalf("wildcard: %d", status)
	}
	for _, host := range []string{"a.customer.com", "b.customer.com"} {
		if status, _, _ = call("POST", "", manager, map[string]string{"hostname": host}); status != 201 {
			t.Fatalf("create %s: %d", host, status)
		}
	}
	status, _, out = call("GET", "?limit=2", reader, nil)
	items, _ := out["items"].([]any)
	if status != 200 || len(items) != 2 || out["next_cursor"] != "b.customer.com" {
		t.Fatalf("first page: %d %v", status, out)
	}
	status, _, out = call("GET", "?limit=2&cursor=b.customer.com", reader, nil)
	items, _ = out["items"].([]any)
	if status != 200 || len(items) != 1 || out["next_cursor"] != "" {
		t.Fatalf("last page: %d %v", status, out)
	}
	if status, _, out = call("GET", "?status=active", reader, nil); status != 200 || len(out["items"].([]any)) != 0 {
		t.Fatalf("status filter: %d %v", status, out)
	}
	if status, _, _ = call("GET", "?status=bogus", reader, nil); status != 400 {
		t.Fatalf("unknown status filter: %d", status)
	}
	if status, _, out = call("GET", "/a.customer.com", reader, nil); status != 200 || out["hostname"] != "a.customer.com" {
		t.Fatalf("show: %d %v", status, out)
	}
	if status, _, _ = call("DELETE", "/a.customer.com", reader, nil); status != 403 {
		t.Fatalf("readers must not delete: %d", status)
	}
	if status, _, out = call("DELETE", "/a.customer.com", manager, nil); status != 202 || out["status"] != "deleting" {
		t.Fatalf("delete: %d %v", status, out)
	}
	if status, _, _ = call("GET", "/missing.customer.com", reader, nil); status != 404 {
		t.Fatalf("missing hostname: %d", status)
	}

	srv.mu.Lock()
	srv.hostnameCreates[app.ID] = hostnameWindow{start: time.Now(), count: hostnameCreatesPerMinute}
	srv.mu.Unlock()
	status, header, _ := call("POST", "", manager, map[string]string{"hostname": "c.customer.com"})
	if status != 429 || header.Get("Retry-After") == "" {
		t.Fatalf("rate limit: %d %v", status, header)
	}
}
