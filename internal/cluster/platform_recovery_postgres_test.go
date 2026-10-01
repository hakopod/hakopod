package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"k8s.io/client-go/kubernetes/fake"
)

func fatalPostgreSQLMigration(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	migration := "unknown"
	if match := regexp.MustCompile(`^migration ([0-9]+):`).FindStringSubmatch(err.Error()); len(match) == 2 {
		migration = match[1]
	}
	if errors.As(err, &pgErr) {
		t.Fatalf("migrate isolated PostgreSQL test store: migration %s, SQLSTATE %s", migration, pgErr.Code)
	}
	t.Fatal("migrate isolated PostgreSQL test store")
}

func isolatedRecoveryStore(t *testing.T) (*store.Store, store.Principal) {
	t.Helper()
	dsn := os.Getenv("HAKOPOD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set HAKOPOD_TEST_DATABASE_URL for isolated real PostgreSQL recovery checks")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil || u.Host == "" || u.Opaque != "" || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatal("HAKOPOD_TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	admin, err := pgx.Connect(ctx, dsn)
	cancel()
	if err != nil {
		t.Fatal("connect to configured PostgreSQL test database")
	}
	database := "hakopod_supabase_recovery_" + store.NewID()
	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
		cancel()
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = admin.Close(closeCtx)
		t.Fatal(err)
	}
	cancel()
	var state *store.Store
	t.Cleanup(func() {
		if state != nil {
			state.Close()
		}
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, dropErr := admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{database}.Sanitize()+" WITH (FORCE)"); dropErr != nil {
			t.Errorf("drop isolated PostgreSQL test database: %v", dropErr)
		}
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if closeErr := admin.Close(closeCtx); closeErr != nil {
			t.Errorf("close PostgreSQL test connection: %v", closeErr)
		}
	})
	u.Path = "/" + database
	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	state, err = store.Open(ctx, u.String())
	if err != nil {
		t.Fatal("open isolated PostgreSQL test store")
	}
	if err = state.Migrate(ctx); err != nil {
		fatalPostgreSQLMigration(t, err)
	}
	raw, err := state.Bootstrap(ctx, "supabase-recovery")
	if err != nil {
		t.Fatal("bootstrap isolated PostgreSQL test store")
	}
	principal, err := state.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal("authenticate isolated PostgreSQL test principal")
	}
	return state, principal
}

func recoveryAuthoritySpec(adminRevision int64) (managedplatform.Spec, map[string]string) {
	resources := map[string]managedplatform.Resources{}
	images := map[string]string{}
	for _, name := range managedplatform.SupabaseComponentNames() {
		resources[name] = managedplatform.Resources{CPU: "500m", Memory: "2Gi"}
		images[name] = "registry.example.test/supabase/" + name + "@sha256:" + strings.Repeat("d", 64)
	}
	secrets := map[string]managedplatform.SecretReference{}
	for _, name := range managedplatform.SupabaseRequiredSecretKeys() {
		secrets[name] = managedplatform.SecretReference{Name: "supabase-" + name, Revision: 1}
	}
	secrets["pooler-api-jwt-secret"] = managedplatform.SecretReference{Name: "supabase-pooler-api-jwt-secret", Revision: adminRevision}
	return managedplatform.Spec{
		SchemaVersion: 1, Name: "supabase-recovery-authority", Kind: "supabase", Version: managedplatform.SupabaseVersion,
		Resources: resources,
		Storage:   map[string]int64{"database": 20, "database-encryption": 1, "edge-functions": 1, "objects": 20, "studio-snippets": 1},
		Secrets:   secrets,
		Placement: managedplatform.Placement{NodeNames: []string{"fixture-node"}},
		Supabase:  &managedplatform.SupabaseConfig{PublicURL: "https://supabase.example.test", SiteURL: "https://app.example.test", DatabaseName: "postgres", JWTExpirySeconds: 3600, RESTMaxRows: 1000, StorageFileLimitBytes: 10 << 20, PoolSize: 10, PoolMaxClients: 100},
	}, images
}

func TestResolveEmptyTargetRejectsChangedPoolerAdministrativeAuthority(t *testing.T) {
	state, principal := isolatedRecoveryStore(t)
	targetSpec, images := recoveryAuthoritySpec(2)
	plan, err := managedplatform.PlanSupabase(targetSpec, images)
	if err != nil {
		t.Fatal(err)
	}
	plan.Capability.Available = true
	plan.Capability.ClusterQualified = true
	target := store.ManagedPlatform{ID: store.NewID(), Project: "demo", Environment: "development", Spec: targetSpec}
	review, err := state.SaveManagedPlatformReview(context.Background(), principal, target, plan, 0, "create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.AcceptManagedPlatform(context.Background(), principal, target, plan, []byte("sealed"), review, 0, "supabase-recovery-authority", "create"); err != nil {
		t.Fatal(err)
	}
	claimed, err := state.ClaimManagedPlatformOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = state.RecordManagedPlatformStep(context.Background(), claimed, "succeeded", "ready", "", map[string]any{"status": "ready"}); err != nil {
		t.Fatal(err)
	}
	sourceSpec, _ := recoveryAuthoritySpec(1)
	sourceJSON, err := json.Marshal(sourceSpec)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &SupabaseRecoveryRuntime{Store: state, Cluster: &Client{kube: fake.NewSimpleClientset()}}
	manifest := platformbackup.Manifest{PlatformID: store.NewID(), PlatformSpec: sourceJSON, Release: managedplatform.SupabaseVersion, Images: images}
	matchingJSON, marshalErr := json.Marshal(targetSpec)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	matching := manifest
	matching.PlatformSpec = matchingJSON
	if controlErr := runtime.ResolveEmptyTarget(context.Background(), platformbackup.Operation{TargetPlatformID: target.ID, ExpectedTargetRevision: 1}, matching); controlErr == nil || controlErr.Error() != "target Supabase namespace ownership is invalid" {
		t.Fatalf("matching administrative authority did not pass the cryptographic compatibility check: %v", controlErr)
	}
	err = runtime.ResolveEmptyTarget(context.Background(), platformbackup.Operation{TargetPlatformID: target.ID, ExpectedTargetRevision: 1}, manifest)
	if err == nil || !strings.Contains(err.Error(), "cryptographic secret revisions differ") {
		t.Fatalf("changed pooler administrative authority was not rejected: %v", err)
	}
}
