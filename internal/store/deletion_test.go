package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

func TestEmptyApplicationDeletionFencesAndHistory(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	app := emptyTestSpec()
	d, err := db.Accept(ctx, p, "demo", "development", app, 0, "delete-initial")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteEmptyApplication(ctx, p, d.ApplicationID, 1, app.Name); !errors.Is(err, ErrConflict) {
		t.Fatal("nonempty application deleted", err)
	}
	empty, _ := spec.Normalize(app)
	empty.Services = map[string]spec.Service{}
	if _, err = db.Accept(ctx, p, "demo", "development", spec.Application{SchemaVersion: 1, Name: "empty-new", Services: map[string]spec.Service{}}, 0, "delete-empty-new"); err == nil {
		t.Fatal("created empty application")
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	removal, err := db.Accept(ctx, p, "demo", "development", empty, 1, "delete-empty-release")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteEmptyApplication(ctx, p, d.ApplicationID, 2, app.Name); !errors.Is(err, ErrConflict) {
		t.Fatal("pending cleanup deleted", err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", removal.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteEmptyApplication(ctx, p, d.ApplicationID, 1, app.Name); !errors.Is(err, ErrConflict) {
		t.Fatal("stale review deleted", err)
	}
	limited := p
	limited.Admin = false
	limited.Permissions = []string{"deployments:write"}
	if err = db.DeleteEmptyApplication(ctx, limited, d.ApplicationID, 2, app.Name); !errors.Is(err, ErrForbidden) {
		t.Fatal("non-admin deleted", err)
	}
	claim, err := db.ClaimRuntime(ctx, d.ApplicationID, 2)
	if err != nil || claim == nil {
		t.Fatal("claim", err)
	}
	if err = db.DeleteEmptyApplication(ctx, p, d.ApplicationID, 2, app.Name); !errors.Is(err, ErrConflict) {
		t.Fatal("active runtime claim deleted", err)
	}
	claim.Release()
	if err = db.DeleteEmptyApplication(ctx, p, d.ApplicationID, 2, app.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Application(ctx, d.ApplicationID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("deleted application visible", err)
	}
	if _, err = db.Deployment(ctx, d.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("history not removed", err)
	}
	if _, err = db.Accept(ctx, p, "demo", "development", app, 0, "delete-reuse-name"); !errors.Is(err, ErrConflict) {
		t.Fatal("retired scope reused", err)
	}
	var audit int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE action='application.delete' AND resource=$1", d.ApplicationID).Scan(&audit); err != nil || audit != 1 {
		t.Fatal("missing deletion audit", err)
	}
}

func TestEmptyProjectDeletionAcrossEnvironments(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()
	_, err := db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('remove-me'); INSERT INTO environments(project,name) VALUES('remove-me','dev'),('remove-me','prod')")
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Accept(ctx, p, "remove-me", "prod", emptyTestSpec(), 0, "delete-project-busy")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteEmptyProject(ctx, p, "remove-me", "remove-me"); !errors.Is(err, ErrConflict) {
		t.Fatal("nonempty project deleted", err)
	}
	_, err = db.Pool.Exec(ctx, "DELETE FROM deployment_events WHERE deployment_id=$1;", d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "DELETE FROM deployments WHERE id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "DELETE FROM applications WHERE id=$1", d.ApplicationID); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteEmptyProject(ctx, p, "remove-me", "wrong"); !errors.Is(err, ErrConflict) {
		t.Fatal("bad confirmation accepted", err)
	}
	if err = db.DeleteEmptyProject(ctx, p, "remove-me", "remove-me"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('remove-me') ON CONFLICT DO NOTHING"); err == nil {
		t.Fatal("retired project reused")
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO runtime_resources(kind,project,environment,name,revision,metadata) VALUES('registry','remove-me','dev','test',1,'{}')"); err == nil {
		t.Fatal("orphan runtime resource created")
	}
}

func TestProjectDeletionSerializesWithApplicationCreation(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('race-delete'); INSERT INTO environments(project,name) VALUES('race-delete','development')"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT name FROM environments WHERE project='race-delete' AND name='development' FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- db.DeleteEmptyProject(ctx, p, "race-delete", "race-delete") }()
	app := emptyTestSpec()
	if _, err = tx.Exec(ctx, "INSERT INTO applications(id,name,project,environment,spec) VALUES('new-during-delete',$1,'race-delete','development',$2)", app.Name, JSON(app)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrConflict) {
		t.Fatal("creation/deletion race lost application", err)
	}
}

func TestDefaultProjectRetainedBeforeOwnerSetup(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	if err := db.DeleteEmptyProject(context.Background(), p, "demo", "demo"); !errors.Is(err, ErrConflict) {
		t.Fatal("default project deleted before owner setup", err)
	}
}

func TestApplicationDeletionSerializesWithBuildDispatch(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	app := emptyTestSpec()
	d, err := db.Accept(ctx, p, "demo", "development", app, 0, "build-delete-initial")
	if err != nil {
		t.Fatal(err)
	}
	empty := app
	empty.Services = map[string]spec.Service{}
	if _, err = db.Pool.Exec(ctx, "UPDATE applications SET spec=$2 WHERE id=$1", d.ApplicationID, JSON(empty)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded',spec=$2 WHERE id=$1", d.ID, JSON(empty)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO build_configs(id,project,environment,name,service,application_id,config,grant_id,created_by) VALUES('build-delete','demo','development',$1,'api',$2,'{}',$3,$4)`, app.Name, d.ApplicationID, p.KeyID, p.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT id FROM build_configs WHERE id='build-delete' FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- db.DeleteEmptyApplication(ctx, p, d.ApplicationID, 1, app.Name) }()
	if _, err = tx.Exec(ctx, `INSERT INTO build_runs(id,build_id,identity_id,key_id,idempotency_key,request_hash,config,config_revision,commit_sha,status) VALUES('active-during-delete','build-delete',$1,$2,'dispatch-race',$3,'{}',1,'commit','dispatching')`, p.ID, p.KeyID, []byte("hash")); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrConflict) {
		t.Fatal("active dispatch was deleted", err)
	}
	var exists bool
	if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM build_runs WHERE id='active-during-delete')").Scan(&exists); err != nil || !exists {
		t.Fatal("active build record lost", err)
	}
}
