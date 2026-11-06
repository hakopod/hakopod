package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/license"
	"github.com/hakopod/hakopod/internal/store"
)

const agentTestDoc = `{"schema_version":1,"description":"Hold production deploys","when":[{"field":"operation","operator":"is_any_of","values":["createDeployment"]},{"field":"environment","operator":"is_any_of","values":["production"]}],"then":{"effect":"require_approval","approval_ttl_seconds":900}}`

func agentCall(t *testing.T, s *Server, p store.Principal, method, path, body string) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	s.registerAgentAccessRoutes(mux)
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func agentErrCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func TestAgentPolicyHandlersRejectBadInputWithoutDatabase(t *testing.T) {
	s := &Server{}
	p := store.Principal{}
	invalid := `{"schema_version":2,"description":"x","when":[],"then":{"effect":"block"}}`
	for _, c := range []struct{ method, path, body, code string }{
		{"POST", "/api/v1/agent-policies", `{`, "invalid_request"},
		{"POST", "/api/v1/agent-policies", `{"name":"a","document":` + agentTestDoc + `,"extra":1}`, "invalid_request"},
		{"POST", "/api/v1/agent-policies", `{"name":"a","document":` + invalid + `}`, "invalid_policy"},
		{"PUT", "/api/v1/agent-policies/x/draft", `{"expected_revision":1,"name":"a","document":` + invalid + `}`, "invalid_policy"},
		{"POST", "/api/v1/agent-policies/preview", `{"days":0,"document":` + agentTestDoc + `}`, "invalid_request"},
		{"POST", "/api/v1/agent-policies/preview", `{"days":31,"document":` + agentTestDoc + `}`, "invalid_request"},
		{"POST", "/api/v1/agent-policies/preview", `{"days":7,"document":` + invalid + `}`, "invalid_policy"},
		{"POST", "/api/v1/agent-policies/x/publish", `{"expected_revision":1,"status":"draft"}`, "invalid_request"},
		{"DELETE", "/api/v1/agent-policies/x", ``, "invalid_request"},
		{"GET", "/api/v1/agent-approvals?limit=51", ``, "invalid_request"},
		{"GET", "/api/v1/agent-approvals?state=all", ``, "invalid_request"},
		{"POST", "/api/v1/agent-approvals/x/deny", `{"reason":"` + strings.Repeat("r", 501) + `"}`, "invalid_request"},
		{"POST", "/api/v1/agent-approvals/x/deny", `{"why":"no"}`, "invalid_request"},
		{"POST", "/api/v1/agent-policies/preview", `{"days":7,"document":` + agentTestDoc + `,"pad":"` + strings.Repeat("a", 70<<10) + `"}`, "invalid_request"},
	} {
		status, out := agentCall(t, s, p, c.method, c.path, c.body)
		if status != 400 || agentErrCode(out) != c.code {
			t.Fatalf("%s %s: got %d %v, want 400 %s", c.method, c.path, status, out, c.code)
		}
	}
	if status, _ := agentCall(t, s, store.Principal{CredentialType: "machine"}, "POST", "/api/v1/agent-policies/simulate", `{"key_id":"k","operation":"createDeployment"}`); status != 403 {
		t.Fatalf("simulate by a machine key: %d", status)
	}
}

func agentTestLicense(t *testing.T, db *store.Store) {
	t.Helper()
	ctx := context.Background()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	db.LicenseVerifier = license.NewVerifier(map[string]ed25519.PublicKey{"ephemeral-fixture": public})
	status, err := db.LicenseStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute).Unix()
	payload, _ := json.Marshal(license.Claims{Version: 1, KeyID: "ephemeral-fixture", LicenseID: strings.Repeat("a", 32), InstallationID: status.InstallationID, Customer: "Local test fixture", Plan: "pro", Sequence: 1, IssuedAt: now, NotBefore: now, ExpiresAt: now + 7200, Features: []string{store.AgentPoliciesFeature}})
	signature := ed25519.Sign(private, append([]byte(license.Domain), payload...))
	token := "hl1." + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature)
	digest := sha256.Sum256([]byte(token))
	if _, err = db.Pool.Exec(ctx, "UPDATE installation_license SET token=$1,token_digest=$2,highest_sequence=1,revision=1 WHERE singleton", token, digest[:]); err != nil {
		t.Fatal(err)
	}
}

func TestAgentPolicyCreatePublishPreviewSimulate(t *testing.T) {
	db := notificationTestDB(t)
	agentTestLicense(t, db)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "agent-access-api-test")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	admin.CredentialType = "browser"
	admin.Email = "admin@example.test"
	s := &Server{Store: db}

	status, list := agentCall(t, s, admin, "GET", "/api/v1/agent-policies", "")
	if status != 200 || list["licensed"] != true {
		t.Fatalf("list: %d %v", status, list)
	}
	status, created := agentCall(t, s, admin, "POST", "/api/v1/agent-policies", `{"name":"Production deploys need approval","document":`+agentTestDoc+`}`)
	if status != 201 {
		t.Fatalf("create: %d %v", status, created)
	}
	id := created["id"].(string)
	rev := int64(created["revision"].(float64))
	status, published := agentCall(t, s, admin, "POST", "/api/v1/agent-policies/"+id+"/publish", `{"expected_revision":`+itoa(rev)+`,"status":"enforced"}`)
	if status != 200 || published["status"] != "enforced" {
		t.Fatalf("publish: %d %v", status, published)
	}
	status, preview := agentCall(t, s, admin, "POST", "/api/v1/agent-policies/preview", `{"days":7,"document":`+agentTestDoc+`}`)
	if status != 200 || preview["evaluated"] != float64(0) || len(preview["samples"].([]any)) != 0 {
		t.Fatalf("preview: %d %v", status, preview)
	}
	status, sim := agentCall(t, s, admin, "POST", "/api/v1/agent-policies/simulate", `{"key_id":"`+admin.KeyID+`","operation":"createDeployment","project":"shop","environment":"production"}`)
	if status != 200 || sim["decision"] != "require_approval" {
		t.Fatalf("simulate: %d %v", status, sim)
	}
	if status, _ := agentCall(t, s, admin, "POST", "/api/v1/agent-policies/simulate", `{"key_id":"missing","operation":"createDeployment"}`); status != 400 {
		t.Fatalf("simulate unknown key: %d", status)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
