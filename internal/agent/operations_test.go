package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestIndependentOperationOptIns(t *testing.T) {
	called := false
	r := func(context.Context, string, string, any, string, any) error { called = true; return nil }
	s := New(r, Scope{"p", "dev"}, true, 32)
	for _, name := range []string{"pod_exec", "database_query"} {
		if _, err := s.Call(context.Background(), name, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("%s enabled by deployment opt-in", name)
		}
	}
	if _, err := s.Call(context.Background(), "api_call", json.RawMessage(`{"operation":"deleteEmptyApplication","path":{"id":"app"}}`)); err == nil {
		t.Fatal("deploy opt-in enabled generic mutations")
	}
	if called {
		t.Fatal("disabled tool reached API")
	}
	s = NewWithOptions(r, Scope{"p", "dev"}, Options{AllowSQL: true}, 32)
	if _, err := s.Call(context.Background(), "database_query", json.RawMessage(`{"database_id":"db","sql":"delete from things","write":true}`)); err == nil || !strings.Contains(err.Error(), "allow-sql-write") {
		t.Fatal(err)
	}
	if called {
		t.Fatal("disabled SQL write reached API")
	}
}
func TestOperationToolDiscoveryExclusions(t *testing.T) {
	s := New(nil, Scope{"p", "dev"}, false, 32)
	out, err := s.Call(context.Background(), "api_operations", json.RawMessage(`{"operation":"mcpMessage"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "exclusion") || !strings.Contains(string(raw), "mcpMessage") {
		t.Fatal("missing explicit contract exclusions")
	}
	for _, tool := range s.Tools() {
		name := tool.(map[string]any)["name"]
		if name == "pod_exec" || name == "database_query" {
			t.Fatalf("disabled tool advertised: %s", name)
		}
	}
}
func TestDeploymentControlScopeAndBody(t *testing.T) {
	calls := []string{}
	r := func(_ context.Context, m, p string, in any, key string, out any) error {
		calls = append(calls, m+" "+p)
		var raw []byte
		switch p {
		case "/deployments/d":
			raw = []byte(`{"application_id":"app"}`)
		case "/applications/app":
			raw = []byte(`{"id":"app","project":"p","environment":"dev","spec":{"services":{"web":{}}}}`)
		default:
			raw = []byte(`{}`)
			if strings.HasSuffix(p, "/rollback") {
				b := in.(map[string]any)
				if b["revision"] != int64(3) || b["expected_revision"] != int64(5) || key != "retry-key" {
					t.Fatal("rollback omitted review inputs")
				}
			}
		}
		return json.Unmarshal(raw, out)
	}
	s := NewWithOptions(r, Scope{"p", "dev"}, Options{AllowDeploy: true}, 32)
	if _, err := s.Call(context.Background(), "cancel_deployment", json.RawMessage(`{"deployment_id":"d"}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ";") != "GET /deployments/d;GET /applications/app;POST /deployments/d/cancel" {
		t.Fatal(calls)
	}
	calls = nil
	if _, err := s.Call(context.Background(), "rollback", json.RawMessage(`{"application_id":"app","revision":3,"expected_revision":5,"idempotency_key":"retry-key"}`)); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatal(calls)
	}
}
func TestPodExecSharedCanonicalBody(t *testing.T) {
	calls := 0
	r := func(_ context.Context, m, p string, in any, _ string, out any) error {
		calls++
		if m == "GET" {
			return json.Unmarshal([]byte(`{"id":"app","project":"p","environment":"dev","spec":{"services":{"web":{}}}}`), out)
		}
		if p != "/applications/app/services/web/exec" {
			t.Fatal(p)
		}
		body := in.(map[string]any)
		if body["timeout_seconds"] != 20 || body["max_output_bytes"] != 65536 || body["pod"] != "pod-1" || body["container"] != "web" {
			t.Fatal(body)
		}
		return json.Unmarshal([]byte(`{"outcome":"exited","exit_code":0}`), out)
	}
	s := NewWithOptions(r, Scope{"p", "dev"}, Options{AllowExec: true}, 0)
	if _, err := s.Call(context.Background(), "pod_exec", json.RawMessage(`{"application_id":"app","service":"web","pod":"pod-1","container":"web","command":["true"]}`)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestReadOnlyToolAnnotations(t *testing.T) {
	s := NewWithOptions(nil, Scope{"p", "dev"}, Options{AllowSQL: true}, 0)
	for _, tool := range s.Tools() {
		m := tool.(map[string]any)
		if m["name"] == "api_call" || m["name"] == "database_query" {
			a := m["annotations"].(map[string]any)
			if a["readOnlyHint"] != true || a["destructiveHint"] != false {
				t.Fatal(m)
			}
		}
	}
}
func TestSQLToolPreservesExactNumericParameters(t *testing.T) {
	r := func(_ context.Context, m, _ string, in any, _ string, out any) error {
		if m == "GET" {
			return json.Unmarshal([]byte(`{"project":"p","environment":"dev"}`), out)
		}
		parameters := in.(map[string]any)["parameters"].([]any)
		if parameters[0] != json.Number("9007199254740993") {
			t.Fatal(parameters)
		}
		return nil
	}
	s := NewWithOptions(r, Scope{"p", "dev"}, Options{AllowSQL: true}, 0)
	if _, err := s.Call(context.Background(), "database_query", json.RawMessage(`{"database_id":"db","sql":"select $1::numeric","parameters":[9007199254740993]}`)); err != nil {
		t.Fatal(err)
	}
}
func TestSQLWritesRequireAndForwardReviewedRevision(t *testing.T) {
	calls := 0
	r := func(_ context.Context, m, _ string, in any, _ string, out any) error {
		calls++
		if m == "GET" {
			return json.Unmarshal([]byte(`{"project":"p","environment":"dev"}`), out)
		}
		body := in.(map[string]any)
		if body["expected_revision"] != int64(7) || body["read_only"] != false {
			t.Fatal(body)
		}
		return nil
	}
	s := NewWithOptions(r, Scope{"p", "dev"}, Options{AllowSQL: true, AllowSQLWrite: true}, 0)
	if _, err := s.Call(context.Background(), "database_query", json.RawMessage(`{"database_id":"db","sql":"update things set value=$1","parameters":[1],"write":true}`)); err == nil || !strings.Contains(err.Error(), "expected_revision") {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("unreviewed write reached API")
	}
	if _, err := s.Call(context.Background(), "database_query", json.RawMessage(`{"database_id":"db","sql":"update things set value=$1","parameters":[1],"write":true,"expected_revision":7}`)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestInstallationAgentDoesNotExposeProjectTools(t *testing.T) {
	s := NewWithOptions(nil, Scope{}, Options{Installation: true, AllowAdmin: true, AllowDeploy: true, AllowExec: true, AllowSQL: true}, 0)
	for _, tool := range s.Tools() {
		name := tool.(map[string]any)["name"]
		if name != "api_operations" && name != "api_call" && name != "audit_export" {
			t.Fatal("project tool on installation connection", name)
		}
	}
	if _, err := s.Call(context.Background(), "applications", json.RawMessage(`{}`)); err == nil {
		t.Fatal("installation connection ran project tool")
	}
}
