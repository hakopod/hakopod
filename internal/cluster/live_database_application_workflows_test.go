package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManagedPostgresApplicationProvisioningLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_APPLICATION_PROVISIONING_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_APPLICATION_PROVISIONING_TEST=1")
	}
	c, ctx := liveRecoveryClient(t)
	d, observed := newRecoveryFixture(t, ctx, c, "postgresql", "17")
	d.Observation = observed
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random)[:12]
	passwordBytes := make([]byte, 32)
	if _, err := rand.Read(passwordBytes); err != nil {
		t.Fatal(err)
	}
	password := []byte(hex.EncodeToString(passwordBytes))
	plan := database.ApplicationProvisioningPlan{SchemaVersion: 1, ID: hex.EncodeToString(random), DatabaseID: d.ID, DatabaseRevision: d.Revision, ApplicationID: "fixture", ApplicationName: "fixture", ApplicationRevision: 1, Service: "main", Variable: "DATABASE_URL", Endpoint: "read_write", Role: "hp_user_" + suffix, LogicalDatabase: "hp_db_" + suffix, SecretReference: "database-password"}
	before := func() error { return nil }
	verified, err := c.ProvisionPostgresApplication(ctx, d, plan, password, "accepted", before)
	if err != nil || verified {
		t.Fatalf("create phase failed or skipped verification handoff: verified=%v err=%v", verified, err)
	}
	verified, err = c.ProvisionPostgresApplication(ctx, d, plan, password, "verifying", before)
	if err != nil || !verified {
		t.Fatalf("native privilege verification failed: verified=%v err=%v", verified, err)
	}
	member := observed.Members[0]
	for _, candidate := range observed.Members {
		if candidate.Role == "primary" {
			member = candidate
		}
	}
	cleanup := fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE); DROP ROLE IF EXISTS %s;", plan.LogicalDatabase, plan.Role)
	defer c.DatabaseExec(context.Background(), d, member, []string{"psql", "-Xq", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-c", cleanup}, nil, io.Discard)
	foreignRole, foreignDB := "hp_foreign_"+suffix, "hp_foreign_db_"+suffix
	foreign := fmt.Sprintf("CREATE ROLE %s LOGIN;", foreignRole)
	if err = c.DatabaseExec(ctx, d, member, []string{"psql", "-Xq", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-c", foreign}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err = c.DatabaseExec(ctx, d, member, []string{"createdb", "-U", "postgres", "-O", foreignRole, foreignDB}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	defer c.DatabaseExec(context.Background(), d, member, []string{"psql", "-Xq", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-c", fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE); DROP ROLE IF EXISTS %s;", foreignDB, foreignRole)}, nil, io.Discard)
	foreignPlan := plan
	foreignPlan.ID = strings.Repeat("f", 32)
	foreignPlan.Role, foreignPlan.LogicalDatabase = foreignRole, foreignDB
	if _, err = c.ProvisionPostgresApplication(ctx, d, foreignPlan, password, "accepted", before); err == nil {
		t.Fatal("unmarked existing PostgreSQL objects were adopted")
	}
}

func TestInfisicalKnexMigrationLockRecoveryLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_MIGRATION_LOCK_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_MIGRATION_LOCK_TEST=1")
	}
	c, ctx := liveRecoveryClient(t)
	d, observed := newRecoveryFixture(t, ctx, c, "postgresql", "17")
	d.Observation = observed
	applicationID := strings.Repeat("a", 32)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(applicationID)}}
	if _, err := c.kube.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	defer c.kube.CoreV1().Namespaces().Delete(context.Background(), ns.Name, metav1.DeleteOptions{})
	member := observed.Members[0]
	for _, candidate := range observed.Members {
		if candidate.Role == "primary" {
			member = candidate
		}
	}
	setup := `DROP TABLE IF EXISTS infisical_migrations_lock; DROP TABLE IF EXISTS infisical_migrations_startup_lock; CREATE TABLE infisical_migrations_lock(index serial primary key,is_locked integer); INSERT INTO infisical_migrations_lock(is_locked) VALUES(1); CREATE TABLE infisical_migrations_startup_lock(index serial primary key,is_locked integer,session_id text,node text,heartbeat_updated_at timestamptz); INSERT INTO infisical_migrations_startup_lock(is_locked) VALUES(0);`
	if err := c.DatabaseExec(ctx, d, member, []string{"psql", "-Xq", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "app", "-c", setup}, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	evidence, err := c.InspectInfisicalMigrationLock(ctx, d, applicationID, 1, "main", "DB_CONNECTION_URI", "app", "docker.io/infisical/infisical:v0.165.10@sha256:204bd63c7a281d9157752ce0bf8d506e7380cac5a0665324eeab8d580b069266")
	if err != nil || !evidence.Repairable() {
		t.Fatalf("abandoned lock was not positively identified: %+v err=%v", evidence, err)
	}
	after, err := c.RepairInfisicalMigrationLock(ctx, d, evidence, func() error { return nil })
	if err != nil || after.KnexLockRows != 1 || after.KnexLockedRows != 0 {
		t.Fatalf("Knex lock was not repaired and verified: %+v err=%v", after, err)
	}
	out := &databaseBoundedWriter{limit: 128}
	if err = c.DatabaseExec(ctx, d, member, []string{"psql", "-XAt", "-U", "postgres", "-d", "app", "-c", "SELECT count(*) FROM infisical_migrations_lock WHERE is_locked=0"}, nil, out); err != nil || strings.TrimSpace(out.String()) != "1" {
		t.Fatal("repair did not preserve exactly one unlocked Knex row")
	}
}
