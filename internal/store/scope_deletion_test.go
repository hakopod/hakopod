package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEmptyEnvironmentDeletionAuthorityConfirmationAndRetirement(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES('demo','discard')"); err != nil {
		t.Fatal(err)
	}
	viewer := p
	viewer.Admin = false
	if err := s.DeleteEmptyEnvironment(ctx, viewer, "demo", "discard", "discard"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if err := s.DeleteEmptyEnvironment(ctx, p, "demo", "discard", "wrong"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	key, _, err := s.CreateKey(ctx, p, KeyInput{Name: "environment-delete", Project: "demo", Environment: "discard", Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEmptyEnvironment(ctx, p, "demo", "discard", "discard"); err != nil {
		t.Fatal(err)
	}
	var revoked, audited bool
	if err := s.Pool.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM api_keys WHERE id=$1", key.ID).Scan(&revoked); err != nil || !revoked {
		t.Fatal("grant was not revoked", err)
	}
	if err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM audit_events WHERE action='environment.delete' AND resource='demo/discard')").Scan(&audited); err != nil || !audited {
		t.Fatal("audit missing", err)
	}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES('demo','discard')"); err == nil {
		t.Fatal("retired environment reused")
	}
}

func TestScopeDeletionRetainsDeletedDatabaseHistoryAndBlocksPendingWork(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "UPDATE identities SET owner=true WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "scope-history-create", "create"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEmptyEnvironment(ctx, p, d.Project, d.Environment, d.Environment); !errors.Is(err, ErrConflict) {
		t.Fatal("live database removed", err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE managed_databases SET deleted_at=now(),status='deleted' WHERE id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEmptyEnvironment(ctx, p, d.Project, d.Environment, d.Environment); !errors.Is(err, ErrConflict) {
		t.Fatal("pending operation ignored", err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE managed_database_operations SET status='succeeded' WHERE database_id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEmptyEnvironment(ctx, p, d.Project, d.Environment, d.Environment); err != nil {
		t.Fatal(err)
	}
	newDatabase := d
	newDatabase.ID = NewID()
	if _, err := s.AcceptDatabase(ctx, p, newDatabase, 0, "create-after-scope-delete", "create"); err == nil {
		t.Fatal("public acceptance recreated resource in deleted scope")
	}
	var exists bool
	if err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM managed_databases WHERE id=$1)", d.ID).Scan(&exists); err != nil || !exists {
		t.Fatal("history lost", err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE managed_databases SET updated_at=now() WHERE id=$1", d.ID); err != nil {
		t.Fatal("history update required deleted parent", err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE managed_databases SET deleted_at=NULL WHERE id=$1", d.ID); err == nil {
		t.Fatal("deleted resource resurrected into missing scope")
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE identities SET owner=true WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEmptyProject(ctx, p, "demo", "demo"); err != nil {
		t.Fatal(err)
	}
}

func TestScopeDeletionSerializesWithScopedResourceInsertion(t *testing.T) {
	for _, kind := range []string{"runtime", "database"} {
		for _, parent := range []string{"environment", "project"} {
			t.Run(kind+"/"+parent, func(t *testing.T) {
				s, p, d := databaseFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if _, err := s.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('race-scope'); INSERT INTO environments(project,name) VALUES('race-scope','test')"); err != nil {
					t.Fatal(err)
				}
				tx, err := s.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				if kind == "runtime" {
					_, err = tx.Exec(ctx, "INSERT INTO runtime_resources(kind,project,environment,name,revision,metadata) VALUES('registry','race-scope','test','fixture',1,'{}')")
				} else {
					_, err = tx.Exec(ctx, "INSERT INTO managed_databases(id,project,environment,name,revision,spec,credentials) VALUES($1,'race-scope','test',$2,1,$3,$4)", d.ID, d.Spec.Name, JSON(d.Spec), []byte("fixture"))
				}
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					if parent == "environment" {
						done <- s.DeleteEmptyEnvironment(ctx, p, "race-scope", "test", "test")
					} else {
						done <- s.DeleteEmptyProject(ctx, p, "race-scope", "race-scope")
					}
				}()
				// The transaction's parent key-share lock makes either deletion wait.
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err = <-done; !errors.Is(err, ErrConflict) {
					t.Fatal("concurrent resource was lost", err)
				}
				if _, err = s.Pool.Exec(ctx, "DELETE FROM runtime_resources WHERE project='race-scope'"); err != nil {
					t.Fatal(err)
				}
				if kind == "database" {
					if _, err = s.Pool.Exec(ctx, "UPDATE managed_databases SET deleted_at=now(),status='deleted' WHERE id=$1", d.ID); err != nil {
						t.Fatal(err)
					}
				}
				if parent == "environment" {
					err = s.DeleteEmptyEnvironment(ctx, p, "race-scope", "test", "test")
				} else {
					err = s.DeleteEmptyProject(ctx, p, "race-scope", "race-scope")
				}
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.Pool.Exec(ctx, "INSERT INTO runtime_resources(kind,project,environment,name,revision,metadata) VALUES('registry','race-scope','test','orphan',1,'{}')")
				var pgerr interface{ SQLState() string }
				if !errors.As(err, &pgerr) || pgerr.SQLState() != "23503" {
					t.Fatal("orphan runtime inserted", err)
				}
				_, err = s.Pool.Exec(ctx, "INSERT INTO managed_databases(id,project,environment,name,revision,spec,credentials) VALUES($1,'race-scope','test',$2,1,$3,$4)", NewID(), d.Spec.Name, JSON(d.Spec), []byte("fixture"))
				if err == nil {
					t.Fatal("orphan managed database inserted")
				}
			})
		}
	}
}

func TestLastDemoEnvironmentRetainedBeforeOwnerSetup(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	if err := s.DeleteEmptyEnvironment(context.Background(), p, "demo", "development", "development"); !errors.Is(err, ErrConflict) {
		t.Fatal("bootstrap environment deleted", err)
	}
}

func TestPersonalWorkspaceKeepsDefaultButAllowsAddedEnvironmentDeletion(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO projects(name) VALUES('personal-test'); INSERT INTO environments(project,name) VALUES('personal-test','development'),('personal-test','staging')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO personal_workspaces(identity_id,project) VALUES($1,'personal-test')", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEmptyEnvironment(ctx, p, "personal-test", "development", "development"); !errors.Is(err, ErrConflict) {
		t.Fatal("personal default removed", err)
	}
	if err := s.DeleteEmptyEnvironment(ctx, p, "personal-test", "staging", "staging"); err != nil {
		t.Fatal("added environment blocked", err)
	}
	if err := s.DeleteEmptyProject(ctx, p, "personal-test", "personal-test"); !errors.Is(err, ErrConflict) {
		t.Fatal("personal project removed", err)
	}
}

func TestEnvironmentDeletionBrowserProjectAdministratorAndScopedKey(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES('demo','staging')"); err != nil {
		t.Fatal(err)
	}
	browser := p
	browser.Admin = false
	browser.Email = "project-admin@example.test"
	browser.CredentialType = "browser"
	browser.ProjectRoles = []ProjectRole{{Project: "demo", Role: "admin"}}
	browser.Permissions = []string{"admin"}
	if err := s.DeleteEmptyEnvironment(ctx, browser, "demo", "staging", "staging"); err != nil {
		t.Fatal("browser project administrator denied", err)
	}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES('demo','private')"); err != nil {
		t.Fatal(err)
	}
	scoped := p
	scoped.Project = "demo"
	scoped.Environment = "private"
	scoped.CredentialType = "machine"
	scoped.Permissions = []string{"deployments:write"}
	if err := s.DeleteEmptyEnvironment(ctx, scoped, "demo", "private", "private"); !errors.Is(err, ErrForbidden) {
		t.Fatal("scoped key gained scope administration", err)
	}
}

func TestProviderScopeCannotAcquireDeletedEnvironment(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES('demo','discard'),('demo','keep'); INSERT INTO secret_providers(name,revision,config,credentials) VALUES('fixture',1,'{}','fixture'); INSERT INTO secret_provider_scopes(provider,project,environments) VALUES('fixture','demo',ARRAY['keep'])"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEmptyEnvironment(ctx, p, "demo", "discard", "discard"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE secret_provider_scopes SET environments=ARRAY['discard'] WHERE provider='fixture'"); err == nil {
		t.Fatal("grant acquired deleted environment")
	}
	if err := s.DeleteEmptyEnvironment(ctx, p, "demo", "keep", "keep"); !errors.Is(err, ErrConflict) {
		t.Fatal("granted environment deleted", err)
	}
	if _, err := s.Pool.Exec(ctx, "UPDATE secret_provider_scopes SET environments='{}' WHERE provider='fixture'"); err != nil {
		t.Fatal("wildcard grant rejected", err)
	}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES('demo','wildcard')"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEmptyEnvironment(ctx, p, "demo", "wildcard", "wildcard"); !errors.Is(err, ErrConflict) {
		t.Fatal("wildcard grant ignored", err)
	}
}

func TestDemoDevelopmentRetainedWithOtherEnvironmentBeforeOwnerSetup(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES('demo','staging')"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEmptyEnvironment(ctx, p, "demo", "development", "development"); !errors.Is(err, ErrConflict) {
		t.Fatal("bootstrap development removed", err)
	}
	if err := s.DeleteEmptyEnvironment(ctx, p, "demo", "staging", "staging"); err != nil {
		t.Fatal("added bootstrap environment blocked", err)
	}
	if _, err := s.SetupOwner(ctx, "Owner", "owner@example.test", "correct horse battery staple", p.ID); err != nil {
		t.Fatal("owner setup failed", err)
	}
	if err := s.DeleteEmptyProject(ctx, p, "demo", "demo"); err != nil {
		t.Fatal("owner could not replace demo", err)
	}
}
