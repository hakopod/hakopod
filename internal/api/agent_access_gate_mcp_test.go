package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/agentaccess"
	"github.com/hakopod/hakopod/internal/operations"
	"github.com/hakopod/hakopod/internal/store"
)

// gatedServer has published policies injected through the cache, so no
// database is needed.
func gatedServer(t *testing.T, policies ...agentaccess.Published) *Server {
	t.Helper()
	s := &Server{}
	c := &s.agentPolicies
	c.policies, c.licensed, c.loaded, c.loadedAt = policies, true, true, time.Now()
	return s
}

func blockPolicy(field, op string, values ...string) agentaccess.Published {
	return agentaccess.Published{ID: "pol_1", Name: "no", Version: 1, Status: agentaccess.StatusEnforced, Document: agentaccess.Document{
		When: []agentaccess.Condition{{Field: field, Operator: op, Values: values}},
		Then: agentaccess.Then{Effect: agentaccess.EffectBlock},
	}}
}

func machine(ctx context.Context) context.Context {
	return context.WithValue(ctx, principalKey{}, store.Principal{ID: "id_1", KeyID: "key_1", CredentialType: "machine", Project: "shop", Environment: "prod"})
}

func listApplicationsID(t *testing.T) string {
	id, _, _, ok := operations.ResolveRoute("GET", "/applications")
	if !ok {
		t.Fatal("GET /applications does not resolve")
	}
	return id
}

func TestMCPApiCallIsGatedByAgentPolicy(t *testing.T) {
	s := gatedServer(t, blockPolicy(agentaccess.FieldOperation, agentaccess.OpIsAnyOf, listApplicationsID(t)))
	reached := 0
	routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++; io.WriteString(w, `{}`) })
	original := httptest.NewRequest("POST", "/mcp", nil)
	original = original.WithContext(machine(original.Context()))
	ctx := s.mcpToolContext(original)
	var out map[string]any
	err := mcpRequester(routes)(ctx, "GET", "/applications", nil, "", &out)
	if err == nil || !strings.Contains(err.Error(), "Blocked by agent policy") || reached != 0 {
		t.Fatalf("blocked call: err=%v reached=%d", err, reached)
	}
	if _, err := mcpStreamRequester(routes)(ctx, "/applications", ""); err == nil || reached != 0 {
		t.Fatalf("blocked stream: err=%v reached=%d", err, reached)
	}
	// A request the policy does not name still runs.
	if err := mcpRequester(routes)(ctx, "GET", "/keys", nil, "", &out); err != nil || reached != 1 {
		t.Fatalf("allowed call: err=%v reached=%d", err, reached)
	}
	// Without the server gate in the context a machine key fails closed.
	bare := context.WithValue(context.Background(), mcpContextKey{}, original)
	if err := mcpRequester(routes)(bare, "GET", "/keys", nil, "", &out); err == nil || reached != 1 {
		t.Fatalf("ungated call: err=%v reached=%d", err, reached)
	}
}

func TestAgentAccessGateHeadAndCandidates(t *testing.T) {
	s := gatedServer(t, blockPolicy(agentaccess.FieldOperation, agentaccess.OpIsAnyOf, listApplicationsID(t)))
	r := httptest.NewRequest("HEAD", "/api/v1/applications", nil)
	if w := httptest.NewRecorder(); s.gateAgentAccess(w, r, store.Principal{CredentialType: "machine", KeyID: "k"}) || w.Code != 403 {
		t.Fatalf("HEAD was not gated as GET: %d", w.Code)
	}
	// The body names another project; the most restrictive candidate wins.
	s = gatedServer(t, blockPolicy(agentaccess.FieldProject, agentaccess.OpIsAnyOf, "other"))
	r = httptest.NewRequest("GET", "/api/v1/applications?project=shop", strings.NewReader(`{"project":"other"}`))
	if w := httptest.NewRecorder(); s.gateAgentAccess(w, r, store.Principal{CredentialType: "machine", KeyID: "k", Project: "shop", Environment: "prod"}) || w.Code != 403 {
		t.Fatalf("restrictive candidate ignored: %d", w.Code)
	}
}

func TestAgentAccessGateLeavesApprovalStatusAlone(t *testing.T) {
	// Every request is blocked, and the cache cannot load, yet the status
	// route still answers.
	s := &Server{}
	s.agentPolicies.failedAt, s.agentPolicies.lastErr = time.Now(), errors.New("down")
	r := httptest.NewRequest("GET", "/api/v1/agent-approvals/apr_1", nil)
	if !s.gateAgentAccess(httptest.NewRecorder(), r, store.Principal{CredentialType: "machine", KeyID: "k"}) {
		t.Fatal("approval status was gated")
	}
	if w := httptest.NewRecorder(); s.gateAgentAccess(w, httptest.NewRequest("GET", "/api/v1/applications", nil), store.Principal{CredentialType: "machine"}) || w.Code != 503 {
		t.Fatalf("failed load did not fail closed: %d", w.Code)
	}
}

func TestAgentPolicyCacheBackoff(t *testing.T) {
	s := &Server{}
	c := &s.agentPolicies
	// A recent failure with a stale-but-usable value serves it without a
	// reload (a reload would dereference the nil store).
	c.policies, c.licensed, c.loaded, c.loadedAt = []agentaccess.Published{{ID: "p"}}, true, true, time.Now().Add(-30*time.Second)
	c.failedAt, c.lastErr = time.Now(), errors.New("down")
	if got, _, err := c.load(context.Background(), s); err != nil || len(got) != 1 {
		t.Fatalf("stale value not served: %v %v", got, err)
	}
	c.loadedAt = time.Now().Add(-2 * time.Minute)
	if _, _, err := c.load(context.Background(), s); err == nil {
		t.Fatal("too-stale value served")
	}
}
