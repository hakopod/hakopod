package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/api"
	dbtypes "github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

type platformTrustRuntime struct {
	mu      sync.Mutex
	calls   int
	failure error
	loaded  store.ManagedPlatform
}

func (*platformTrustRuntime) ReconcileManagedPlatform(context.Context, *store.Store, store.ManagedPlatformOperation) error {
	return nil
}

func (runtime *platformTrustRuntime) ManagedPlatformTrust(ctx context.Context, item store.ManagedPlatform) (dbtypes.PublicTrust, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.calls++
	runtime.loaded = item
	deadline, bounded := ctx.Deadline()
	if !bounded || time.Until(deadline) > 5*time.Second {
		return dbtypes.PublicTrust{}, errors.New("missing trust-read timeout")
	}
	if runtime.failure != nil {
		return dbtypes.PublicTrust{}, runtime.failure
	}
	return dbtypes.PublicTrust{CertificatePEM: "PUBLIC_CA_CERTIFICATE", Fingerprint: strings.Repeat("a", 64), Issuer: "Managed platform fixture"}, nil
}

func TestManagedPlatformTrustScopeAndPublicProjection(t *testing.T) {
	db, _ := database(t)
	ctx := context.Background()
	owner, err := db.Bootstrap(ctx, "platform-trust-owner")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES('demo','other')"); err != nil {
		t.Fatal(err)
	}
	_, foreign, err := db.CreateKey(ctx, principal, store.KeyInput{Name: "foreign-reader", Project: "demo", Environment: "other", Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	_, reader, err := db.CreateKey(ctx, principal, store.KeyInput{Name: "platform-reader", Project: "demo", Environment: "development", Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	id := store.NewID()
	spec := managedplatform.Spec{SchemaVersion: 1, Name: "trust-fixture", Kind: "supabase", Version: "0.8.2"}
	_, err = db.Pool.Exec(ctx, `INSERT INTO managed_platforms(id,project,environment,name,kind,revision,desired_spec,status)
		VALUES($1,'demo','development','trust-fixture','supabase',1,$2,'ready')`, id, store.JSON(spec))
	if err != nil {
		t.Fatal(err)
	}
	runtime := &platformTrustRuntime{}
	server := httptest.NewServer((&api.Server{Store: db, ManagedPlatformRuntime: runtime}).Handler())
	defer server.Close()
	request := func(token, resource string) (int, http.Header, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/managed-platforms/"+resource+"/trust?project=other&environment=other", nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, response.Header, string(body)
	}
	for _, test := range []struct {
		token, id string
		status    int
	}{{"", id, 401}, {foreign, id, 404}, {owner, store.NewID(), 404}} {
		status, _, _ := request(test.token, test.id)
		if status != test.status {
			t.Fatalf("trust read status=%d want=%d", status, test.status)
		}
	}
	runtime.mu.Lock()
	calls := runtime.calls
	runtime.mu.Unlock()
	if calls != 0 {
		t.Fatal("runtime trust reached before resource authorization")
	}
	status, headers, body := request(reader, id)
	if status != 200 || headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("scoped trust read status=%d cache=%q", status, headers.Get("Cache-Control"))
	}
	var public map[string]any
	if err := json.Unmarshal([]byte(body), &public); err != nil {
		t.Fatal(err)
	}
	if len(public) != 5 || public["certificate_pem"] != "PUBLIC_CA_CERTIFICATE" {
		t.Fatal("public trust response changed its bounded projection")
	}
	runtime.mu.Lock()
	loaded := runtime.loaded
	runtime.failure = errors.New("private-fixture-detail")
	runtime.mu.Unlock()
	if loaded.ID != id || loaded.Project != "demo" || loaded.Environment != "development" {
		t.Fatal("trust provider used caller query scope instead of loaded resource")
	}
	status, _, body = request(owner, id)
	if status != 503 || strings.Contains(body, "private-fixture-detail") {
		t.Fatal("failed trust read exposed provider details")
	}
}
