package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
)

const validRecoveryBody = `{"kind":"backup","project":"demo","environment":"development","source_platform_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","destination_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","destination_revision":1,"expected_source_revision":1}`

func recoveryPrincipal() store.Principal {
	return store.Principal{ID: "identity", KeyID: "key", Admin: true, Project: "demo", Environment: "development", Permissions: []string{"deployments:write"}}
}

func recoveryRequest(path, body string, principal store.Principal) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "capacity-guard-1")
	return request.WithContext(context.WithValue(request.Context(), principalKey{}, principal))
}

func recoveryHandlers(server *Server) []struct {
	name, path string
	handler    func(http.ResponseWriter, *http.Request)
} {
	return []struct {
		name, path string
		handler    func(http.ResponseWriter, *http.Request)
	}{{"review", "/api/v1/managed-platform-recovery/reviews", server.reviewPlatformRecovery}, {"accept", "/api/v1/managed-platform-recovery/operations", server.acceptPlatformRecovery}}
}

func TestPlatformRecoveryClosedGateFollowsRequestAndScopeAuthorization(t *testing.T) {
	server := &Server{}
	for _, test := range recoveryHandlers(server) {
		t.Run(test.name+"/malformed", func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, recoveryRequest(test.path, `{}`, recoveryPrincipal()))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("malformed recovery returned %d %s", response.Code, response.Body.String())
			}
		})
		t.Run(test.name+"/unauthorized", func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, recoveryRequest(test.path, validRecoveryBody, store.Principal{ID: "identity", KeyID: "key"}))
			if response.Code != http.StatusForbidden {
				t.Fatalf("unauthorized recovery returned %d %s", response.Code, response.Body.String())
			}
		})
		t.Run(test.name+"/authorized", func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, recoveryRequest(test.path, validRecoveryBody, recoveryPrincipal()))
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "managed_platform_recovery_unavailable") {
				t.Fatalf("closed qualification gate returned %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestManagedCloudCapacityGuardRejectsAuthorizedRecoveryWrites(t *testing.T) {
	server := &Server{Store: &store.Store{ManagedCloud: true}}
	for _, test := range recoveryHandlers(server) {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, recoveryRequest(test.path, validRecoveryBody, recoveryPrincipal()))
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "managed_platform_capacity_unavailable") {
				t.Fatalf("capacity guard returned %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestPlatformRecoveryQualificationUsesLoadedKindAndExactTarget(t *testing.T) {
	db := notificationTestDB(t)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO projects(name) VALUES('demo') ON CONFLICT DO NOTHING; INSERT INTO environments(project,name) VALUES('demo','development') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	platform := func(name, kind string) string {
		t.Helper()
		id := store.NewID()
		// These records exercise API authorization and kind selection only. No
		// synthetic observation is used to qualify a runtime or recovery path.
		if _, err := db.Pool.Exec(ctx, `INSERT INTO managed_platforms(id,project,environment,name,kind,revision,desired_spec) VALUES($1,'demo','development',$2,$3,1,$4)`, id, name, kind, store.JSON(map[string]any{"name": name, "kind": kind})); err != nil {
			t.Fatal(err)
		}
		return id
	}
	neon, supabase := platform("neon-source", "neon"), platform("supabase-source", "supabase")
	neonTarget, supabaseTarget := platform("neon-target", "neon"), platform("supabase-target", "supabase")
	for _, test := range []struct {
		name, source, target, confirmation string
		neonQualified, supabaseQualified   bool
		sourceRevision, targetRevision     int64
		want                               int
	}{
		{"neon-closed", neon, "", "", false, true, 1, 0, http.StatusServiceUnavailable},
		{"supabase-closed", supabase, "", "", true, false, 1, 0, http.StatusServiceUnavailable},
		{"neon-qualified", neon, "", "", true, false, 1, 0, http.StatusOK},
		{"supabase-qualified", supabase, "", "", false, true, 1, 0, http.StatusOK},
		{"stale-source", neon, "", "", true, false, 2, 0, http.StatusConflict},
		{"same-kind-target", neon, neonTarget, "neon-target", true, false, 1, 1, http.StatusOK},
		{"different-kind-target", neon, supabaseTarget, "supabase-target", true, true, 1, 1, http.StatusConflict},
		{"wrong-target-name", neon, neonTarget, "other-target", true, false, 1, 1, http.StatusConflict},
		{"stale-target", neon, neonTarget, "neon-target", true, false, 1, 2, http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := platformRecoveryRequest{Intent: platformbackup.Intent{Kind: "backup", Project: "demo", Environment: "development", SourcePlatformID: test.source, ExpectedSourceRevision: test.sourceRevision, DestinationID: strings.Repeat("b", 32), DestinationRevision: 1}}
			if test.target != "" {
				in.Kind, in.TargetPlatformID, in.ExpectedTargetRevision = "restore", test.target, test.targetRevision
				in.ArtifactID, in.DestinationID, in.DestinationRevision = strings.Repeat("c", 32), "", 0
				in.ConfirmTargetName = test.confirmation
			}
			server := &Server{Store: db, ManagedNeonRecoveryQualified: test.neonQualified, ManagedPlatformRecoveryQualified: test.supabaseQualified}
			response := httptest.NewRecorder()
			_, ok := server.validatePlatformRecoveryRequest(response, recoveryRequest("/api/v1/managed-platform-recovery/reviews", "", recoveryPrincipal()), in)
			if response.Code != test.want || ok != (test.want == http.StatusOK) {
				t.Fatalf("recovery selection returned %d, accepted=%v; want %d: %s", response.Code, ok, test.want, response.Body.String())
			}
		})
	}
}
