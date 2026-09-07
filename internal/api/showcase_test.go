package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

func showcaseOwner(t *testing.T, db *store.Store) store.Principal {
	t.Helper()
	p, err := db.SetupOwner(context.Background(), "Sample owner", "sample-owner@example.test", "showcase fixture password long", "")
	if err != nil {
		t.Fatal(err)
	}
	session, err := db.NewSession(context.Background(), p.ID, "browser", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return session.User
}
func TestShowcaseFreshOwnerDurabilityAndReviewedRemoval(t *testing.T) {
	db := sourceDatabase(t)
	db.ShowcaseEnabled = true
	ctx := context.Background()
	owner := showcaseOwner(t, db)
	item, err := db.Showcase(ctx)
	if err != nil || item.State != "queued" || !item.Sample {
		t.Fatal("first owner did not atomically schedule sample", item, err)
	}
	grant, err := db.KeyPrincipal(ctx, item.GrantID)
	if err != nil {
		t.Fatal(err)
	}
	if !grant.Allows("deployments:write", "demo", "development", "shop") || grant.Allows("deployments:write", "demo", "development", "other") || grant.IsAdmin() {
		t.Fatal("sample grant escaped its exact application scope")
	}
	server := &Server{Store: db}
	if err = server.processShowcase(ctx); err != nil {
		t.Fatal(err)
	}
	accepted, err := db.Showcase(ctx)
	if err != nil || accepted.State != "accepted" || accepted.ApplicationID == "" || accepted.DeploymentStatus != "queued" {
		t.Fatal("sample did not expose actual accepted deployment", accepted, err)
	}
	// Simulate losing only the bookkeeping write after durable acceptance.
	if _, err = db.Pool.Exec(ctx, "UPDATE showcase SET state='queued',application_id='',deployment_id='' WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	if err = server.processShowcase(ctx); err != nil {
		t.Fatal(err)
	}
	replay, _ := db.Showcase(ctx)
	if replay.ApplicationID != accepted.ApplicationID || replay.DeploymentID != accepted.DeploymentID {
		t.Fatal("restart replay duplicated sample")
	}
	var count int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM applications").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate sample application", count, err)
	}
	machine := owner
	machine.CredentialType = "machine"
	if err = db.RequestShowcaseRemoval(ctx, machine, replay.Revision, replay.ApplicationID, replay.ApplicationRevision); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("machine credential removed bootstrap sample", err)
	}
	if err = db.RequestShowcaseRemoval(ctx, owner, replay.Revision, "another-app", replay.ApplicationRevision); !errors.Is(err, store.ErrConflict) {
		t.Fatal("unreviewed identity accepted", err)
	}
	if err = db.RequestShowcaseRemoval(ctx, owner, replay.Revision, replay.ApplicationID, replay.ApplicationRevision+1); !errors.Is(err, store.ErrConflict) {
		t.Fatal("unreviewed application revision accepted", err)
	}
	if err = db.RequestShowcaseRemoval(ctx, owner, replay.Revision, replay.ApplicationID, replay.ApplicationRevision); err != nil {
		t.Fatal(err)
	}
	removing, _ := db.Showcase(ctx)
	if removing.State != "removing" || removing.Revision <= replay.Revision {
		t.Fatal("removal task not durable")
	}
	if _, err = db.KeyPrincipal(ctx, item.GrantID); err == nil {
		t.Fatal("removal failed to revoke bootstrap deployment grant")
	}
	changed := removing
	changed.RemoveRevision++
	if err = db.DetachShowcase(ctx, changed); !errors.Is(err, store.ErrConflict) {
		t.Fatal("changed sample revision detached", err)
	}
	if err = db.DetachShowcase(ctx, removing); err != nil {
		t.Fatal(err)
	}
	if err = db.DetachShowcase(ctx, removing); err != nil {
		t.Fatal("recovery after metadata removal failed", err)
	}
	after, _ := db.Showcase(ctx)
	if after.ApplicationID != replay.ApplicationID || after.ApplicationRevision != 0 {
		t.Fatal("cleanup lost original tracked namespace identity")
	}
	replacement, _ := spec.Showcase()
	d, err := db.Accept(ctx, owner, "demo", "development", replacement, 0, "replacement-after-sample")
	if err != nil {
		t.Fatal(err)
	}
	if d.ApplicationID == after.ApplicationID {
		t.Fatal("replacement reused removed identity")
	}
	if err = db.DetachShowcase(ctx, after); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Application(ctx, d.ApplicationID); err != nil {
		t.Fatal("cleanup touched a same-name replacement", err)
	}
}
func TestShowcaseCancelledBeforeAcceptanceNeverRecreates(t *testing.T) {
	db := sourceDatabase(t)
	db.ShowcaseEnabled = true
	owner := showcaseOwner(t, db)
	ctx := context.Background()
	item, _ := db.Showcase(ctx)
	if err := db.RequestShowcaseRemoval(ctx, owner, item.Revision, "", 0); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: db}
	for i := 0; i < 2; i++ {
		if err := server.processShowcase(ctx); err != nil {
			t.Fatal(err)
		}
	}
	item, _ = db.Showcase(ctx)
	if item.State != "removed" || item.Removable {
		t.Fatal("cancelled sample was recreated", item)
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM applications").Scan(&count); err != nil || count != 0 {
		t.Fatal("cancelled sample accepted", count, err)
	}
}
func TestShowcaseDoesNotRetrofitExistingInstallations(t *testing.T) {
	t.Run("existing-owner", func(t *testing.T) {
		db := sourceDatabase(t)
		showcaseOwner(t, db)
		db.ShowcaseEnabled = true
		ctx := context.Background()
		if err := (&Server{Store: db}).processShowcase(ctx); err != nil {
			t.Fatal(err)
		}
		item, _ := db.Showcase(ctx)
		if item.Sample {
			t.Fatal("startup seeded an existing installation")
		}
	})
	t.Run("legacy-owner-conversion", func(t *testing.T) {
		db := sourceDatabase(t)
		ctx := context.Background()
		raw, err := db.Bootstrap(ctx, "legacy")
		if err != nil {
			t.Fatal(err)
		}
		p, err := db.Authenticate(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		db.ShowcaseEnabled = true
		if _, err = db.SetupOwner(ctx, "Legacy owner", "legacy@example.test", "legacy fixture password long", p.ID); err != nil {
			t.Fatal(err)
		}
		item, _ := db.Showcase(ctx)
		if item.Sample {
			t.Fatal("legacy owner conversion seeded an existing installation")
		}
	})
}

func TestShowcaseAcceptanceMarkerFailureRollsBackAllDesiredState(t *testing.T) {
	db := sourceDatabase(t)
	db.ShowcaseEnabled = true
	showcaseOwner(t, db)
	ctx := context.Background()
	item, _ := db.Showcase(ctx)
	_, err := db.Pool.Exec(ctx, `CREATE FUNCTION fail_sample_marker() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='accepted' THEN RAISE EXCEPTION 'fixture marker failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_sample_marker BEFORE UPDATE ON showcase FOR EACH ROW EXECUTE FUNCTION fail_sample_marker()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.AcceptShowcase(ctx, item); err == nil {
		t.Fatal("injected marker failure did not fail acceptance")
	}
	var apps, deployments, events int
	if err = db.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM applications),(SELECT count(*) FROM deployments),(SELECT count(*) FROM deployment_events)").Scan(&apps, &deployments, &events); err != nil || apps != 0 || deployments != 0 || events != 0 {
		t.Fatal("marker failure left orphaned desired state", apps, deployments, events, err)
	}
	after, _ := db.Showcase(ctx)
	if after.State != "queued" || after.ApplicationID != "" || after.Revision != item.Revision {
		t.Fatal("failed transaction changed the sample marker")
	}
	if _, err = db.Pool.Exec(ctx, "DROP TRIGGER fail_sample_marker ON showcase; DROP FUNCTION fail_sample_marker()"); err != nil {
		t.Fatal(err)
	}
	d, err := db.AcceptShowcase(ctx, item)
	if err != nil {
		t.Fatal("acceptance did not recover after rollback", err)
	}
	after, _ = db.Showcase(ctx)
	if after.ApplicationID != d.ApplicationID || after.DeploymentID != d.ID || after.State != "accepted" {
		t.Fatal("recovered acceptance omitted its atomic marker")
	}
}

func TestShowcaseLostAcceptanceConnectionAndConcurrentCancellation(t *testing.T) {
	db := sourceDatabase(t)
	db.ShowcaseEnabled = true
	owner := showcaseOwner(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	item, _ := db.Showcase(ctx)
	const fixtureLock = 724899999
	blocker, err := db.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		if _, err := blocker.Exec(clean, "SELECT pg_advisory_unlock_all()"); err != nil {
			_ = blocker.Hijack().Close(clean)
		} else {
			blocker.Release()
		}
	}()
	if _, err = blocker.Exec(ctx, "SELECT pg_advisory_lock($1)", int64(fixtureLock)); err != nil {
		t.Fatal(err)
	}
	// Pause after the application INSERT but before the deployment can commit.
	if _, err = db.Pool.Exec(ctx, `CREATE FUNCTION pause_sample_acceptance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(724899999::bigint); RETURN NEW; END $$; CREATE TRIGGER pause_sample_acceptance BEFORE INSERT ON deployments FOR EACH ROW EXECUTE FUNCTION pause_sample_acceptance()`); err != nil {
		t.Fatal(err)
	}
	accepted := make(chan error, 1)
	go func() { _, err := db.AcceptShowcase(ctx, item); accepted <- err }()
	waiter := func(lock int64) int32 {
		t.Helper()
		for {
			var pid int32
			err := db.Pool.QueryRow(ctx, "SELECT pid FROM pg_locks WHERE locktype='advisory' AND objid=$1::oid AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND NOT granted LIMIT 1", lock).Scan(&pid)
			if err == nil {
				return pid
			}
			if ctx.Err() != nil {
				t.Fatal("fixture transaction did not reach expected lock", ctx.Err())
			}
			time.Sleep(15 * time.Millisecond)
		}
	}
	pid := waiter(fixtureLock)
	removed := make(chan error, 1)
	go func() { removed <- db.RequestShowcaseRemoval(ctx, owner, item.Revision, "", 0) }()
	_ = waiter(store.ShowcaseLock)
	var killed bool
	if err = db.Pool.QueryRow(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&killed); err != nil || !killed {
		t.Fatal("could not terminate isolated acceptance connection", err)
	}
	select {
	case err = <-accepted:
		if err == nil {
			t.Fatal("lost acceptance connection reported success")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err = <-removed:
		if err != nil {
			t.Fatal("cancellation did not proceed after transaction rollback", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	after, _ := db.Showcase(ctx)
	if after.State != "removing" || after.ApplicationID != "" {
		t.Fatal("connection loss produced an untracked sample", after)
	}
	if _, err = db.AcceptShowcase(ctx, item); err == nil {
		t.Fatal("stale cancelled task was accepted")
	}
	if err = (&Server{Store: db}).processShowcase(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ = db.Showcase(ctx)
	var count int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM applications").Scan(&count); err != nil || count != 0 || after.State != "removed" {
		t.Fatal("cancelled sample survived connection failure", count, after.State, err)
	}
}
