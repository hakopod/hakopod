package main

import (
	"context"
	"strings"
	"testing"
)

func TestVirtualNetworksCommand(t *testing.T) {
	c, _ := networkTestServer(t, func(method, path, body string) (int, string) {
		switch method + " " + path {
		case "GET /api/v1/virtual-networks":
			return 200, `{"items":[{"name":"backend","description":"Shared data","segments":["db","web"],"revision":3}],"can_manage":false}`
		case "GET /api/v1/virtual-networks/backend":
			return 200, `{"network":{"name":"backend"},"toml":"name = \"backend\"\n","connections":[]}`
		}
		t.Errorf("unexpected %s %s", method, path)
		return 404, `{"error":{"code":"not_found","message":"no"}}`
	})
	cfg := config{Project: "shop", Environment: "prod"}
	var out strings.Builder
	if err := virtualNetworksCommand(context.Background(), c, cfg, nil, false, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "backend\trevision 3\tsegments: db, web\tShared data\n" {
		t.Fatalf("list output %q", out.String())
	}
	out.Reset()
	if err := virtualNetworksCommand(context.Background(), c, cfg, []string{"backend"}, false, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "name = \"backend\"\n" {
		t.Fatalf("network output %q", out.String())
	}
	if err := virtualNetworksCommand(context.Background(), c, cfg, []string{"a", "b"}, false, &out); exitCode(err) != 2 {
		t.Fatalf("two names: %v", err)
	}
}

func TestNetworkAccessErrorNamesAdministratorOnlyForApply(t *testing.T) {
	denied := &exitError{3, "forbidden: credential does not allow this operation"}
	if msg := networkAccessError(denied, true).Error(); !strings.Contains(msg, "requires a project administrator") {
		t.Fatalf("apply: %s", msg)
	}
	msg := networkAccessError(denied, false).Error()
	if strings.Contains(msg, "administrator") || !strings.Contains(msg, "read access") {
		t.Fatalf("plan: %s", msg)
	}
}
