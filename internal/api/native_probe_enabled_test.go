//go:build hakopod_native_acceptance && linux

package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

func TestNativeProbeRequiresAdmittedRunBeforeDatabaseAccess(t *testing.T) {
	mux := http.NewServeMux()
	(&Server{}).registerNativeProbeRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/managed-platforms/fixture/native-probe", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured provider probe reached database access: %d", w.Code)
	}
}

func TestNativeProbeAdmissionRequiresExactWritableReadyNeonRevision(t *testing.T) {
	platformID := strings.Repeat("a", 32)
	item := store.ManagedPlatform{ID: platformID, Project: "native-neon", Environment: "development", Revision: 4, Status: "ready", Spec: managedplatform.Spec{Kind: "neon"}}
	principal := store.Principal{Project: item.Project, Environment: item.Environment, Permissions: []string{"deployments:write"}, IdentityPermissions: []string{"deployments:write"}}
	if err := nativeProbeAdmission(principal, item, item.Revision); err != nil {
		t.Fatalf("exact admitted platform was rejected: %v", err)
	}
	cases := []struct {
		name     string
		p        store.Principal
		item     store.ManagedPlatform
		revision int64
		want     error
	}{
		{name: "invalid revision", p: principal, item: item, revision: 0, want: store.ErrInput},
		{name: "wrong project", p: store.Principal{Project: "other", Environment: item.Environment, Permissions: []string{"deployments:write"}}, item: item, revision: item.Revision, want: store.ErrForbidden},
		{name: "wrong environment", p: store.Principal{Project: item.Project, Environment: "production", Permissions: []string{"deployments:write"}}, item: item, revision: item.Revision, want: store.ErrForbidden},
		{name: "read only", p: store.Principal{Project: item.Project, Environment: item.Environment, Permissions: []string{"deployments:read"}, IdentityPermissions: []string{"deployments:read"}}, item: item, revision: item.Revision, want: store.ErrForbidden},
		{name: "identity read only", p: store.Principal{Project: item.Project, Environment: item.Environment, Permissions: []string{"deployments:write"}, IdentityPermissions: []string{"deployments:read"}}, item: item, revision: item.Revision, want: store.ErrForbidden},
		{name: "stale revision", p: principal, item: item, revision: item.Revision - 1, want: store.ErrConflict},
		{name: "not ready", p: principal, item: func() store.ManagedPlatform { value := item; value.Status = "creating"; return value }(), revision: item.Revision, want: store.ErrConflict},
		{name: "wrong kind", p: principal, item: func() store.ManagedPlatform { value := item; value.Spec.Kind = "supabase"; return value }(), revision: item.Revision, want: store.ErrConflict},
		{name: "deleted", p: principal, item: func() store.ManagedPlatform { value := item; now := time.Now(); value.DeletedAt = &now; return value }(), revision: item.Revision, want: store.ErrConflict},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := nativeProbeAdmission(test.p, test.item, test.revision); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}

func TestNativeProbeSnapshotAdmissionRejectsNonterminalOrMismatchedOperation(t *testing.T) {
	item := store.ManagedPlatform{ID: strings.Repeat("a", 32), Revision: 4}
	accepted := store.ManagedPlatformOperation{PlatformID: item.ID, Revision: item.Revision, Kind: "create", Status: "succeeded"}
	if err := nativeProbeSnapshotAdmission(item, accepted); err != nil {
		t.Fatalf("accepted snapshot was rejected: %v", err)
	}
	cases := []store.ManagedPlatformOperation{
		{PlatformID: item.ID, Revision: item.Revision, Kind: "create", Status: "queued"},
		{PlatformID: item.ID, Revision: item.Revision, Kind: "create", Status: "failed"},
		{PlatformID: item.ID, Revision: item.Revision, Kind: "delete", Status: "succeeded"},
		{PlatformID: strings.Repeat("b", 32), Revision: item.Revision, Kind: "create", Status: "succeeded"},
		{PlatformID: item.ID, Revision: item.Revision + 1, Kind: "update", Status: "succeeded"},
	}
	for _, op := range cases {
		if err := nativeProbeSnapshotAdmission(item, op); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("operation %#v returned %v, want conflict", op, err)
		}
	}
}

func TestNativeProbeConcurrencyIsOne(t *testing.T) {
	s := &Server{nativeProbes: make(chan struct{}, 1)}
	release, ok := s.acquireNativeProbe()
	if !ok {
		t.Fatal("first native probe was not admitted")
	}
	if secondRelease, secondOK := s.acquireNativeProbe(); secondOK || secondRelease != nil {
		t.Fatal("second concurrent native probe was admitted")
	}
	release()
	thirdRelease, thirdOK := s.acquireNativeProbe()
	if !thirdOK {
		t.Fatal("native probe slot was not released")
	}
	thirdRelease()
}
