package api

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/agentaccess"
	"github.com/hakopod/hakopod/internal/store"
)

func TestAgentAccessGateBodyRestore(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/v1/keys", strings.NewReader(`{"project":"shop"}`))
	body, tooLarge, err := gateBody(r)
	if err != nil || tooLarge || string(body) != `{"project":"shop"}` {
		t.Fatalf("body %q %v %v", body, tooLarge, err)
	}
	if again, _ := io.ReadAll(r.Body); string(again) != `{"project":"shop"}` || r.ContentLength != int64(len(again)) {
		t.Fatalf("handler saw %q", again)
	}
	big := bytes.Repeat([]byte("a"), agentGateBodyLimit+10)
	r = httptest.NewRequest("POST", "/api/v1/keys", bytes.NewReader(big))
	if body, tooLarge, _ = gateBody(r); !tooLarge || body != nil {
		t.Fatal("over-limit body was accepted")
	}
	if again, _ := io.ReadAll(r.Body); len(again) != len(big) {
		t.Fatalf("handler saw %d of %d bytes", len(again), len(big))
	}
}

func TestAgentAccessGateScope(t *testing.T) {
	s := &Server{}
	key := store.Principal{Project: "keyproj", Environment: "keyenv"}
	r := httptest.NewRequest("GET", "/api/v1/x?project=q&environment=qe", nil)
	got, err := s.gateScope(context.Background(), r, []string{"x"}, gateFields{Project: "b", Environment: "be"}, key)
	want := []gateCandidate{{"q", "qe"}, {"b", "be"}, {"keyproj", "keyenv"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("candidates %v %v", got, err)
	}
	r = httptest.NewRequest("POST", "/api/v1/x", nil)
	got, _ = s.gateScope(context.Background(), r, []string{"x"}, parseGateFields([]byte(`{"project":"keyproj"}`)), key)
	if !slices.Equal(got, []gateCandidate{{"keyproj", "keyenv"}}) {
		t.Fatalf("deduped candidates %v", got)
	}
	for path, want := range map[string][2]string{
		"/builds/b1/runs/r1/deploy":   {"builds", "b1"},
		"/deployments/d1/cancel":      {"deployments", "d1"},
		"/backup-artifacts/a1":        {"backup-artifacts", "a1"},
		"/external-databases/e1":      {"external-databases", "e1"},
		"/keys/k1":                    {"keys", "k1"},
		"/previews/p1":                {"previews", "p1"},
		"/projects/shop/environments": {"projects", "shop"},
	} {
		segs, _ := gateSegments("/api/v1" + path)
		if table, id := gateScopeResource(segs, gateFields{}); table != want[0] || id != want[1] {
			t.Fatalf("%s resource %s %s", path, table, id)
		}
	}
	if table, id := gateScopeResource([]string{"plans"}, gateFields{ApplicationID: "a1"}); table != "applications" || id != "a1" {
		t.Fatalf("body resource %s %s", table, id)
	}
	if segs, ok := gateSegments("/api/v1/applications/a%2Fb/x"); !ok || !slices.Equal(segs, []string{"applications", "a/b", "x"}) {
		t.Fatalf("segments %q", segs)
	}
}

func TestAgentAccessCredentialTTL(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pol := agentaccess.Published{ID: "p1", Name: "short keys", Status: agentaccess.StatusEnforced, Document: agentaccess.Document{Then: agentaccess.Then{MaxCredentialTTLSeconds: 3600}}}
	d := agentaccess.Decision{Matches: []agentaccess.Match{{PolicyID: "p1", Status: agentaccess.StatusEnforced}}}
	for _, c := range []struct {
		f   gateFields
		bad bool
	}{
		{gateFields{ExpiresAt: now.Add(30 * time.Minute).Format(time.RFC3339)}, false},
		{gateFields{ExpiresAt: now.Add(2 * time.Hour).Format(time.RFC3339)}, true},
		{gateFields{}, true},
		{gateFields{NeverExpires: true, ExpiresAt: now.Format(time.RFC3339)}, true},
	} {
		if _, _, bad := credentialTTLViolation("createKey", d, []agentaccess.Published{pol}, c.f, now); bad != c.bad {
			t.Fatalf("%+v: bad=%v", c.f, bad)
		}
	}
	if _, _, bad := credentialTTLViolation("listKeys", d, []agentaccess.Published{pol}, gateFields{}, now); bad {
		t.Fatal("non-key operation capped")
	}
	d.Matches[0].Exception = true
	if _, _, bad := credentialTTLViolation("createKey", d, []agentaccess.Published{pol}, gateFields{}, now); bad {
		t.Fatal("exception match capped")
	}
}

func TestAgentAccessResponses(t *testing.T) {
	w := httptest.NewRecorder()
	writePolicyBlocked(w, &agentaccess.Match{Name: "no prod", Version: 3})
	if w.Code != 403 || !strings.Contains(w.Body.String(), `"policy_blocked"`) || !strings.Contains(w.Body.String(), `Blocked by agent policy \"no prod\" v3.`) {
		t.Fatalf("block %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	exp := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	writeApprovalRequired(w, "prod deploys", store.AgentApproval{ID: "apr_1", ExpiresAt: exp})
	if w.Code != 403 || w.Header().Get("Hakopod-Approval-Id") != "apr_1" || w.Header().Get("Hakopod-Approval-Expires") != "2026-01-01T01:00:00Z" ||
		!strings.Contains(w.Body.String(), `"approval_required"`) || !strings.Contains(w.Body.String(), "Hakopod-Approval: apr_1") {
		t.Fatalf("approval %d %v %s", w.Code, w.Header(), w.Body)
	}
}

func TestAgentAccessGateSkipsHumansAndQueueIsSafe(t *testing.T) {
	s := &Server{}
	if !s.gateAgentAccess(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/keys", nil), store.Principal{CredentialType: "browser"}) {
		t.Fatal("browser principal was gated")
	}
	for i := 0; i < agentEvaluationQueue+5; i++ {
		s.agentPolicies.record(store.AgentAccessEvaluation{})
	}
	if s.agentPolicies.dropped.Load() != 5 {
		t.Fatalf("dropped %d", s.agentPolicies.dropped.Load())
	}
}
