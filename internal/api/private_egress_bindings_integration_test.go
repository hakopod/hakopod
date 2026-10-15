package api_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/store"
)

func TestPrivateEgressBindingAdminAPI(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "private-egress-api-test")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := db.CreateKey(ctx, principal, store.KeyInput{Name: "reader", Permissions: []string{"deployments:read"}, Project: "demo", Environment: "development", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&api.Server{Store: db}).Handler())
	defer server.Close()
	request := func(method, path, token string, body any) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+"/api/v1/admin/private-egress-bindings"+path, bytes.NewReader(store.JSON(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(data)
	}
	input := map[string]any{"project": "demo", "environment": "development", "application": "shop", "services": []string{"api"}, "cidrs": []string{"172.31.0.0/20"}, "ports": []int{5432}, "expected_revision": 0}
	if code, _ := request("PUT", "/orders-db", reader, input); code != 403 {
		t.Fatalf("non-admin write status: %d", code)
	}
	if code, _ := request("GET", "", reader, nil); code != 403 {
		t.Fatalf("non-admin read status: %d", code)
	}
	if code, body := request("PUT", "/orders-db", raw, input); code != 201 || !strings.Contains(body, `"revision":1`) {
		t.Fatalf("create: %d %s", code, body)
	}
	if code, _ := request("PUT", "/orders-db", raw, input); code != 409 {
		t.Fatalf("stale revision status: %d", code)
	}
	bad := map[string]any{"project": "demo", "environment": "development", "application": "shop", "services": []string{"api"}, "cidrs": []string{"10.42.0.0/24"}, "ports": []int{5432}, "expected_revision": 0}
	if code, _ := request("PUT", "/pods", raw, bad); code != 400 {
		t.Fatalf("reserved range status: %d", code)
	}
	if code, body := request("GET", "/orders-db", raw, nil); code != 200 || !strings.Contains(body, "172.31.0.0/20") {
		t.Fatalf("get: %d %s", code, body)
	}
	if code, _ := request("DELETE", "/orders-db", raw, map[string]any{"expected_revision": 0}); code != 409 {
		t.Fatalf("stale delete status: %d", code)
	}
	if code, _ := request("DELETE", "/orders-db", raw, map[string]any{"expected_revision": 1}); code != 200 {
		t.Fatalf("delete status: %d", code)
	}
	if code, _ := request("GET", "/orders-db", raw, nil); code != 404 {
		t.Fatalf("deleted binding still readable: %d", code)
	}
}
