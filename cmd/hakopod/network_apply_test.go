package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

func networkTestServer(t *testing.T, handle func(method, path, body string) (int, string)) (*client, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		status, out := handle(r.Method, r.URL.EscapedPath(), string(b))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(out))
	}))
	t.Cleanup(server.Close)
	return &client{url: server.URL, key: "k", http: server.Client()}, &calls
}

func testNetwork(apps ...string) spec.VirtualNetwork {
	return spec.VirtualNetwork{SchemaVersion: 1, Name: "backend", Segments: map[string]spec.NetworkSegment{"db": {Applications: apps}}}
}

func planFixture(t *testing.T, next spec.VirtualNetwork, prev *spec.VirtualNetwork, id string, rev int64) string {
	b, err := json.Marshal(networkPlan{Spec: next, Previous: prev, TOML: "x", ExpectedID: id, ExpectedRevision: rev})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const networkID = "0123456789abcdef0123456789abcdef"

func TestNetworkCreate(t *testing.T) {
	next := testNetwork("api")
	c, calls := networkTestServer(t, func(method, path, body string) (int, string) {
		switch method + " " + path {
		case "POST /api/v1/virtual-networks/plan":
			if body != `{"environment":"prod","project":"shop","toml":"name = \"backend\"\n"}` {
				t.Errorf("plan body = %s", body)
			}
			return 200, planFixture(t, next, nil, "", 0)
		case "POST /api/v1/virtual-networks":
			want := `{"environment":"prod","expected_revision":0,"project":"shop","spec":{"schema_version":1,"name":"backend","description":"","segments":{"db":{"applications":["api"]}}}}`
			if body != want {
				t.Errorf("create body = %s", body)
			}
			return 201, `{"name":"backend","revision":1}`
		}
		t.Errorf("unexpected %s %s", method, path)
		return 500, `{}`
	})
	p, err := planNetwork(context.Background(), c, "shop", "prod", []byte("name = \"backend\"\n"))
	if err != nil || p.Previous != nil || p.Unchanged() {
		t.Fatalf("plan = %+v, %v", p, err)
	}
	res, err := applyNetwork(context.Background(), c, "shop", "prod", p)
	if err != nil || res != (networkResult{Name: "backend", Action: "created", Revision: 1}) || *calls != 2 {
		t.Fatalf("apply = %+v, %v, calls %d", res, err, *calls)
	}
}

func TestNetworkUpdateSendsExpectation(t *testing.T) {
	prev := testNetwork("api")
	c, _ := networkTestServer(t, func(method, path, body string) (int, string) {
		want := `{"environment":"prod","expected_id":"` + networkID + `","expected_revision":3,"project":"shop","spec":{"schema_version":1,"name":"backend","description":"","segments":{"db":{"applications":["api","worker"]}}}}`
		if method != "PUT" || path != "/api/v1/virtual-networks/backend" || body != want {
			t.Errorf("update = %s %s %s", method, path, body)
		}
		return 200, `{"name":"backend","revision":4}`
	})
	res, err := applyNetwork(context.Background(), c, "shop", "prod", networkPlan{Spec: testNetwork("api", "worker"), Previous: &prev, ExpectedID: networkID, ExpectedRevision: 3})
	if err != nil || res != (networkResult{Name: "backend", Action: "updated", Revision: 4}) {
		t.Fatalf("apply = %+v, %v", res, err)
	}
}

func TestNetworkUnchangedMakesNoWrite(t *testing.T) {
	prev := testNetwork("api")
	c, calls := networkTestServer(t, func(method, path, body string) (int, string) {
		t.Errorf("unexpected write %s %s", method, path)
		return 500, `{}`
	})
	res, err := applyNetwork(context.Background(), c, "shop", "prod", networkPlan{Spec: testNetwork("api"), Previous: &prev, ExpectedID: networkID, ExpectedRevision: 3})
	if err != nil || *calls != 0 || res != (networkResult{Name: "backend", Action: "unchanged", Revision: 3}) {
		t.Fatalf("apply = %+v, %v, calls %d", res, err, *calls)
	}
}

func TestNetworkConflictSurfacesServerError(t *testing.T) {
	prev := testNetwork("api")
	c, _ := networkTestServer(t, func(method, path, body string) (int, string) {
		return 409, `{"error":{"code":"conflict","message":"network changed since review"}}`
	})
	_, err := applyNetwork(context.Background(), c, "shop", "prod", networkPlan{Spec: testNetwork(), Previous: &prev, ExpectedID: networkID, ExpectedRevision: 3})
	var e *exitError
	if !errors.As(err, &e) || e.code != 4 || e.message != "conflict: network changed since review" {
		t.Fatalf("err = %v", err)
	}
}
