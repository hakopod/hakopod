package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/store"
)

func nativeHoldFixture(t *testing.T) (*Server, store.ActionsPool, store.ActionsSlot, store.Principal, string, store.ActionsProviderHold) {
	t.Helper()
	s, fake, pool, slot, owner, key := nativeWorkflowFixture(t)
	first := fake.jobs[slot.ProviderRunnerID]
	second := first
	second.Identity.JobID += "-second"
	h := nativeHistory(t, s, pool, slot)
	if err := s.Store.HoldActionsProviderReuse(context.Background(), pool.ApplicationID, pool.Service, h, []actions.ProviderJob{first, second}); err != nil {
		t.Fatal(err)
	}
	state, err := s.Store.ActionsProviderHold(context.Background(), owner, pool.ApplicationID, pool.Service)
	if err != nil || state.Hold == nil {
		t.Fatal("fixture hold missing", err)
	}
	return s, pool, slot, owner, key, *state.Hold
}

func nativeHoldRequest(handler http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestActionsProviderHoldInspectionIsScopedAndRetainsEvidence(t *testing.T) {
	s, pool, slot, owner, key, hold := nativeHoldFixture(t)
	ctx := context.Background()
	if _, err := s.Store.Pool.Exec(ctx, `INSERT INTO environments(project,name) VALUES($1,'production')`, pool.Project); err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	base := "/api/v1/applications/" + pool.ApplicationID + "/actions/" + pool.Service + "/hold"
	for _, test := range []struct {
		name, permission, environment, application string
		status                                     int
	}{
		{"read", "deployments:read", pool.Environment, pool.ApplicationName, 200},
		{"logs", "logs:read", pool.Environment, pool.ApplicationName, 403},
		{"write", "deployments:write", pool.Environment, pool.ApplicationName, 403},
		{"wrong-environment", "deployments:read", "production", pool.ApplicationName, 403},
		{"wrong-application", "deployments:read", pool.Environment, "another-application", 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, scoped, err := s.Store.CreateKey(ctx, owner, store.KeyInput{Name: test.name, Project: pool.Project, Environment: test.environment, Application: test.application, Permissions: []string{test.permission}, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			response := nativeHoldRequest(handler, "GET", base, scoped, "")
			if response.Code != test.status {
				t.Fatalf("hold access = %d %s", response.Code, response.Body.String())
			}
			if response.Code != 200 && strings.Contains(response.Body.String(), hold.Jobs[0].Identity.JobID) {
				t.Fatal("denied request exposed hold evidence")
			}
		})
	}
	response := nativeHoldRequest(handler, "GET", base, key, "")
	var state store.ActionsProviderHoldState
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil || response.Code != 200 || state.Hold == nil || state.Hold.ID != hold.ID || state.ActiveSlots != 1 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("inspection omitted hold or cleanup status", err)
	}
	for _, forbidden := range []string{"original-management", "original-jobs", "provider_config", gitlabLifecycleToken} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatal("inspection leaked private provider configuration")
		}
	}
	// Synthetic deletion represents completed provider cleanup and history expiry.
	if _, err := s.Store.Pool.Exec(ctx, `DELETE FROM actions_slots WHERE id=$1`, slot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Pool.Exec(ctx, `DELETE FROM actions_jobs WHERE slot_id=$1`, slot.ID); err != nil {
		t.Fatal(err)
	}
	response = nativeHoldRequest(handler, "GET", base, key, "")
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil || response.Code != 200 || state.Hold == nil || state.Hold.ID != hold.ID || len(state.Hold.Jobs) != 2 || state.ActiveSlots != 0 {
		t.Fatal("history expiry made the incident uninspectable", err)
	}
	response = nativeHoldRequest(handler, "GET", strings.Replace(base, "/actions/"+pool.Service+"/", "/actions/missing/", 1), key, "")
	if response.Code != 404 {
		t.Fatal("missing service fell back to another pool")
	}
}

func TestActionsProviderHoldReleaseAPIRequiresReviewAndCurrentWriteAccess(t *testing.T) {
	s, pool, slot, owner, key, hold := nativeHoldFixture(t)
	ctx := context.Background()
	handler := s.Handler()
	base := "/api/v1/applications/" + pool.ApplicationID + "/actions/" + pool.Service + "/hold"
	body := `{"hold_id":"` + hold.ID + `","acknowledge":true}`
	for _, invalid := range []string{"", "null", `{}`, `{"hold_id":"` + hold.ID + `"}`, `{"hold_id":"` + hold.ID + `","acknowledge":false}`, `{"hold_id":"broken","acknowledge":true}`, body + `{}`, strings.TrimSuffix(body, "}") + `,"unknown":true}`} {
		response := nativeHoldRequest(handler, "POST", base+"/release", key, invalid)
		if response.Code != 400 {
			t.Fatalf("invalid acknowledgement = %d %s", response.Code, response.Body.String())
		}
	}
	response := nativeHoldRequest(handler, "POST", base+"/release", key, body)
	if response.Code != 409 || !strings.Contains(response.Body.String(), "cleanup") {
		t.Fatal("release accepted an active runner slot")
	}
	if _, err := s.Store.Pool.Exec(ctx, `DELETE FROM actions_slots WHERE id=$1`, slot.ID); err != nil {
		t.Fatal(err)
	}
	metadata, writer, err := s.Store.CreateKey(ctx, owner, store.KeyInput{Name: "revoked-hold-writer", Project: pool.Project, Environment: pool.Environment, Application: pool.ApplicationName, Permissions: []string{"deployments:write"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.Store.Authenticate(ctx, writer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1`, metadata.ID); err != nil {
		t.Fatal(err)
	}
	// Invoke the handler with a previously authenticated principal to exercise
	// revocation after middleware authentication but before the release write.
	r := httptest.NewRequest("POST", base+"/release", strings.NewReader(body))
	r.SetPathValue("id", pool.ApplicationID)
	r.SetPathValue("service", pool.Service)
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal))
	w := httptest.NewRecorder()
	s.actionsProviderHoldRelease(w, r)
	if w.Code != 403 {
		t.Fatalf("revoked cached principal release = %d %s", w.Code, w.Body.String())
	}
	_, reader, err := s.Store.CreateKey(ctx, owner, store.KeyInput{Name: "hold-reader", Project: pool.Project, Environment: pool.Environment, Application: pool.ApplicationName, Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	response = nativeHoldRequest(handler, "POST", base+"/release", reader, body)
	if response.Code != 403 {
		t.Fatal("read-only key released the hold")
	}
	response = nativeHoldRequest(handler, "POST", base+"/release", key, body)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"status":"released"`) || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("confirmed release = %d %s", response.Code, response.Body.String())
	}
	response = nativeHoldRequest(handler, "POST", base+"/release", key, body)
	if response.Code != 409 {
		t.Fatal("replayed acknowledgement released an absent incident")
	}
	response = nativeHoldRequest(handler, "GET", base, key, "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"hold":null`) {
		t.Fatal("released hold remained active")
	}
}
