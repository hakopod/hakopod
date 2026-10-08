package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/sandbox"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/jackc/pgx/v5"
)

func sessionFixture(t *testing.T) (*Store, Principal, Principal, Deployment, sandbox.CreateRequest, string) {
	t.Helper()
	db := isolatedDatabase(t)
	admin := bootstrapPrincipal(t, db)
	ctx := context.Background()
	app := emptyTestSpec()
	app.Services = map[string]spec.Service{"worker": {Image: "example/worker@sha256:" + strings.Repeat("a", 64), Command: []string{"worker"}, RunAsUser: 1000, RunAsGroup: 1000, FSGroup: 1000, ReadOnlyRootFilesystem: true, RuntimeProfile: "sandbox", Session: &spec.SandboxSession{AllowedIdentities: []string{admin.ID}, ReadyCommand: []string{"ready"}, HelperCommand: []string{"helper"}}}}
	normalized, err := spec.Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	db.ValidateDeployment = func(context.Context, Application, spec.Application) error { return nil }
	d, err := db.Accept(ctx, admin, "demo", "development", normalized, 0, "session-fixture", normalized)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := db.Claim(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if err = claim.Finish(ctx, "succeeded", "", nil); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	_, raw, err := db.CreateKey(ctx, admin, KeyInput{Name: "session-client", Project: "demo", Environment: "development", Application: app.Name, Permissions: []string{"sessions:create", "sessions:read", "sessions:call", "sessions:delete"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := sandbox.HashKey("tenant-a")
	return db, admin, p, d, sandbox.CreateRequest{ExpectedRevision: 1, ExpectedImage: app.Services["worker"].Image, RuntimeKey: "notebook-a"}, owner
}
func TestSessionConcurrentAdmissionAndOwnerBoundary(t *testing.T) {
	db, admin, p, d, in, owner := sessionFixture(t)
	ctx := context.Background()
	ids := make(chan string, 8)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := db.CreateSession(ctx, p, d.ApplicationID, "worker", owner, "create-once", in)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- r.ID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	count := 0
	for value := range ids {
		count++
		if id != "" && id != value {
			t.Fatal("duplicate session")
		}
		id = value
	}
	if count != 8 {
		t.Fatal("concurrent create failed", count)
	}
	if _, err := db.ReadSession(ctx, admin, d.ApplicationID, "worker", owner, id, "sessions:read"); !errors.Is(err, ErrForbidden) {
		t.Fatal("administrator bypassed machine authority", err)
	}
	other, _ := sandbox.HashKey("tenant-b")
	if _, err := db.ReadSession(ctx, p, d.ApplicationID, "worker", other, id, "sessions:read"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("cross-owner receipt exposed", err)
	}
	if _, err := db.CreateSession(ctx, p, d.ApplicationID, "worker", owner, "create-different", in); !errors.Is(err, ErrConflict) {
		t.Fatal("same owner runtime duplicated", err)
	}
	changed := in
	changed.RuntimeKey = "different"
	if _, err := db.CreateSession(ctx, p, d.ApplicationID, "worker", owner, "create-once", changed); !errors.Is(err, ErrConflict) {
		t.Fatal("idempotency conflict ignored", err)
	}
	a, err := db.Application(ctx, d.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Accept(ctx, admin, "demo", "development", a.Spec, a.Revision, "blocked-session-deploy", a.Spec); !errors.Is(err, ErrConflict) {
		t.Fatal("deployment overtook session", err)
	}
}
func TestSessionGenerationCallReplayAndCancellation(t *testing.T) {
	db, _, p, d, in, owner := sessionFixture(t)
	ctx := context.Background()
	created, err := db.CreateSession(ctx, p, d.ApplicationID, "worker", owner, "create-session", in)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := db.ClaimSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CheckSessionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveSessionRuntime(ctx, lease, sandbox.RuntimeState{NamespaceUID: "ns-a", PodUID: "pod-a", ContainerID: "container-a", Ready: true, ImageID: "image-fixture"}); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveSessionRuntime(ctx, lease, sandbox.RuntimeState{NamespaceUID: "ns-a", PodUID: "pod-b", ContainerID: "container-a", Ready: true, ImageID: "image-fixture"}); !errors.Is(err, ErrClaimLost) {
		t.Fatal("pod replacement accepted", err)
	}
	if err = db.SaveSessionRuntime(ctx, lease, sandbox.RuntimeState{NamespaceUID: "ns-a", PodUID: "pod-a", ContainerID: "container-a", ImageID: "replacement-image", Ready: true}); !errors.Is(err, ErrClaimLost) {
		t.Fatal("runtime image replacement accepted", err)
	}
	if _, err = db.ClaimSessionCall(ctx, p, d.ApplicationID, "worker", owner, created.ID, "wrong-generation", "call-1", strings.Repeat("a", 64)); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong generation accepted", err)
	}
	call, err := db.ClaimSessionCall(ctx, p, d.ApplicationID, "worker", owner, created.ID, created.Generation, "call-1", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CheckSessionCall(ctx, call); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ClaimSessionCall(ctx, p, d.ApplicationID, "worker", owner, created.ID, created.Generation, "call-2", strings.Repeat("b", 64)); !errors.Is(err, ErrConflict) {
		t.Fatal("parallel call admitted", err)
	}
	if err = db.FinishSessionCall(ctx, call, true); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ClaimSessionCall(ctx, p, d.ApplicationID, "worker", owner, created.ID, created.Generation, "call-1", strings.Repeat("a", 64)); !errors.Is(err, ErrConflict) {
		t.Fatal("completed call replayed", err)
	}
	call, err = db.ClaimSessionCall(ctx, p, d.ApplicationID, "worker", owner, created.ID, created.Generation, "call-2", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CloseSession(ctx, p, d.ApplicationID, "worker", owner, created.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.CheckSessionCall(ctx, call); !errors.Is(err, ErrConflict) {
		t.Fatal("cancelled call remained authorized", err)
	}
	if _, err = db.HeartbeatSession(ctx, p, d.ApplicationID, "worker", owner, created.ID, created.Generation); !errors.Is(err, ErrConflict) {
		t.Fatal("heartbeat revived cancelled session", err)
	}
	if err = db.SessionCleaned(ctx, lease); err != nil {
		t.Fatal(err)
	}
	r, err := db.ReadSession(ctx, p, d.ApplicationID, "worker", owner, created.ID, "sessions:read")
	if err != nil || r.Status != sandbox.Closed || r.CleanupPending || r.ClosedAt == nil {
		t.Fatal("cleanup not durable", r, err)
	}
	var outcome string
	if err = db.Pool.QueryRow(ctx, `SELECT outcome FROM sandbox_session_calls WHERE session_id=$1 AND request_id='call-2'`, created.ID).Scan(&outcome); err != nil || outcome != "interrupted" {
		t.Fatal("cancelled call was not marked uncertain", outcome, err)
	}
}
func TestSessionExpiryAndRevocation(t *testing.T) {
	db, _, p, d, in, owner := sessionFixture(t)
	ctx := context.Background()
	first, err := db.CreateSession(ctx, p, d.ApplicationID, "worker", owner, "session-first", in)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := db.ClaimSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE sandbox_sessions SET idle_until=now()-interval '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.HeartbeatSession(ctx, p, d.ApplicationID, "worker", owner, first.ID, first.Generation); !errors.Is(err, ErrConflict) {
		t.Fatal("expired session revived", err)
	}
	if err = db.CheckSessionLease(ctx, lease); !errors.Is(err, ErrConflict) {
		t.Fatal("expired session retained authority", err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE sandbox_sessions SET idle_until=now()+interval '10 minutes' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1`, p.KeyID); err != nil {
		t.Fatal(err)
	}
	if err = db.CheckSessionLease(ctx, lease); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked creator retained authority", err)
	}
	if _, err = db.ReadSession(ctx, p, d.ApplicationID, "worker", owner, first.ID, "sessions:read"); !errors.Is(err, ErrForbidden) {
		t.Fatal("stale authenticated principal retained access", err)
	}
}

func TestSessionConcurrentQuotaAndLostLease(t *testing.T) {
	db, _, p, d, in, owner := sessionFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	accepted := make(chan string, 12)
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			request := in
			request.RuntimeKey = fmt.Sprintf("runtime-%d", n)
			r, err := db.CreateSession(ctx, p, d.ApplicationID, "worker", owner, fmt.Sprintf("create-session-%d", n), request)
			if err == nil {
				accepted <- r.ID
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		}(n)
	}
	wg.Wait()
	close(accepted)
	count := 0
	for range accepted {
		count++
	}
	if count != sandbox.MaxSessions {
		t.Fatal("concurrent admissions exceeded quota", count)
	}
	lease, err := db.ClaimSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE sandbox_sessions SET lease_until=now()-interval '1 second' WHERE id=$1`, lease.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveSessionRuntime(ctx, lease, sandbox.RuntimeState{NamespaceUID: "old", PodUID: "old", ContainerID: "old", Ready: true, ImageID: "image-fixture"}); !errors.Is(err, ErrClaimLost) {
		t.Fatal("expired lease changed runtime", err)
	}
	if err = db.CheckSessionLease(ctx, lease); !errors.Is(err, ErrClaimLost) {
		t.Fatal("expired lease retained side-effect authority", err)
	}
}

func TestSessionReceiptDeletionWaitsForCleanup(t *testing.T) {
	db, _, p, d, in, owner := sessionFixture(t)
	ctx := context.Background()
	created, err := db.CreateSession(ctx, p, d.ApplicationID, "worker", owner, "delete-fixture", in)
	if err != nil {
		t.Fatal(err)
	}
	app, err := db.Application(ctx, d.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = deleteApplicationMetadata(ctx, tx, app); !errors.Is(err, ErrConflict) {
		t.Fatal("active session did not fence application removal", err)
	}
	_ = tx.Rollback(ctx)
	if _, err = db.Pool.Exec(ctx, "UPDATE sandbox_sessions SET status='closed',cleanup_pending=false,closed_at=now() WHERE id=$1", created.ID); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = deleteApplicationMetadata(ctx, tx, app); err != nil {
		t.Fatal("cleaned session receipt prevented application removal", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM sandbox_sessions WHERE application_id=$1", d.ApplicationID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("session receipt remained", remaining, err)
	}
}

func TestSessionCleanupDeadlinePrecedesNewStartup(t *testing.T) {
	db, _, p, d, in, owner := sessionFixture(t)
	ctx := context.Background()
	expired, err := db.CreateSession(ctx, p, d.ApplicationID, "worker", owner, "expired-fixture", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE sandbox_sessions SET status='ready',idle_until=now()-interval '1 second' WHERE id=$1", expired.ID); err != nil {
		t.Fatal(err)
	}
	in.RuntimeKey = "another-runtime"
	if _, err = db.CreateSession(ctx, p, d.ApplicationID, "worker", owner, "startup-fixture", in); err != nil {
		t.Fatal(err)
	}
	lease, err := db.ClaimSession(ctx)
	if err != nil || lease.ID != expired.ID {
		t.Fatal("new startup delayed expired-session cleanup", lease.ID, err)
	}
}
