package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/invocation"
	"github.com/hakopod/hakopod/internal/spec"
)

func invocationFixture(t *testing.T) (*Store, Principal, Principal, Deployment, invocation.CreateRequest, []byte) {
	t.Helper()
	db := isolatedDatabase(t)
	admin := bootstrapPrincipal(t, db)
	ctx := context.Background()
	app := emptyTestSpec()
	app.Services = map[string]spec.Service{"report": {Image: "busybox@sha256:" + strings.Repeat("a", 64), Job: &spec.Job{TimeoutSeconds: 60, Invocation: &spec.JobInvocation{AllowedIdentities: []string{admin.ID}, InputKeys: []string{"payload"}, QueueLimit: 16}}}}
	normalized, err := spec.Normalize(app)
	if err != nil {
		t.Fatal(err)
	}
	db.ValidateDeployment = func(context.Context, Application, spec.Application) error { return nil }
	d, err := db.Accept(ctx, admin, "demo", "development", normalized, 0, "invocation-fixture", normalized)
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
	expires := time.Now().Add(time.Hour)
	_, raw, err := db.CreateKey(ctx, admin, KeyInput{Name: "job-client", Project: "demo", Environment: "development", Application: app.Name, Permissions: []string{"jobs:invoke", "jobs:read", "jobs:cancel", "jobs:logs"}, ExpiresAt: expires})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	in := invocation.CreateRequest{ExpectedImage: app.Services["report"].Image, ExpectedRevision: 1, CorrelationID: "report-one", OwnerScope: "team-one", Inputs: map[string]json.RawMessage{"payload": json.RawMessage(`{"value":42}`)}}
	return db, admin, p, d, in, bytes.Repeat([]byte{9}, 32)
}

func TestInvocationDurableScopeIdempotencyAndRecovery(t *testing.T) {
	db, admin, p, d, in, key := invocationFixture(t)
	ctx := context.Background()
	owner, _ := invocation.OwnerHash(in.OwnerScope)
	var ids []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := db.EnqueueInvocation(ctx, p, d.ApplicationID, "report", "duplicate-request", in, key)
			if e != nil {
				t.Error(e)
				return
			}
			mu.Lock()
			ids = append(ids, r.ID)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(ids) != 8 {
		t.Fatal(ids)
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatal("duplicate invocation")
		}
	}
	r, err := db.ReadInvocation(ctx, p, d.ApplicationID, "report", owner, ids[0], "jobs:read")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := invocation.Open(key, r.ID, "input", r.EncryptedInput)
	if err != nil || !bytes.Contains(raw, []byte("42")) {
		t.Fatal("encrypted input mismatch", err)
	}
	encoded, _ := json.Marshal(r)
	if bytes.Contains(encoded, []byte("payload")) || bytes.Contains(encoded, []byte("encrypted")) {
		t.Fatal("receipt exposed input")
	}
	changed := in
	changed.CorrelationID = "different"
	if _, err = db.EnqueueInvocation(ctx, p, d.ApplicationID, "report", "duplicate-request", changed, key); !errors.Is(err, ErrConflict) {
		t.Fatal("changed idempotency input accepted", err)
	}
	if _, err = db.ReadInvocation(ctx, admin, d.ApplicationID, "report", owner, r.ID, "jobs:read"); !errors.Is(err, ErrForbidden) {
		t.Fatal("admin bypassed exact scope", err)
	}
	otherOwner, _ := invocation.OwnerHash("another-team")
	if _, err = db.ReadInvocation(ctx, p, d.ApplicationID, "report", otherOwner, r.ID, "jobs:read"); err == nil {
		t.Fatal("cross-owner read")
	}
	list, err := db.ListInvocations(ctx, p, d.ApplicationID, "report", owner, in.CorrelationID, "duplicate-request", true)
	if err != nil || len(list) != 1 {
		t.Fatal("recovery lookup failed", err)
	}
	c, err := db.ClaimInvocation(ctx)
	if err != nil || c == nil {
		t.Fatal("claim failed", err)
	}
	if err = c.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if second, err := db.ClaimInvocation(ctx); err != nil || second != nil {
		t.Fatal("parallel app claim", err)
	}
	if _, err = db.Accept(ctx, admin, "demo", "development", d.Spec, 1, "must-not-overlap"); !errors.Is(err, ErrConflict) {
		t.Fatal("deployment overtook invocation", err)
	}
	if err = c.Save(ctx, invocation.RuntimeState{Status: invocation.Running, NamespaceUID: "namespace-one", RuntimeUID: "job-one"}, nil); err != nil {
		t.Fatal(err)
	}
	c.Release()
	c, err = db.ClaimInvocation(ctx)
	if err != nil || c == nil || c.Record.RuntimeUID != "job-one" {
		t.Fatal("receipt recovery failed", err)
	}
	defer c.Release()
	if _, err = db.CancelInvocation(ctx, p, d.ApplicationID, "report", owner, r.ID); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(c.Check(ctx), ErrInvocationCancelled) {
		t.Fatal("cancel did not revoke effects")
	}
	if err = c.Save(ctx, invocation.RuntimeState{Status: invocation.Cancelled, Message: "Cancelled"}, nil); err != nil {
		t.Fatal(err)
	}
	if !c.Record.CleanupPending {
		t.Fatal("terminal record bypassed cleanup")
	}
	if _, err = db.Accept(ctx, admin, "demo", "development", d.Spec, 1, "still-cleaning"); !errors.Is(err, ErrConflict) {
		t.Fatal("deployment bypassed cleanup", err)
	}
	if err = c.Cleaned(ctx); err != nil {
		t.Fatal(err)
	}
	c.Release()
	if _, err = db.Accept(ctx, admin, "demo", "development", d.Spec, 1, "after-cleanup"); err != nil {
		t.Fatal(err)
	}
}

func TestInvocationAdmissionQueueAndRevokedAuthority(t *testing.T) {
	db, admin, p, d, in, key := invocationFixture(t)
	ctx := context.Background()
	wrong := in
	wrong.ExpectedImage = "busybox@sha256:" + strings.Repeat("b", 64)
	if _, err := db.EnqueueInvocation(ctx, p, d.ApplicationID, "report", "wrong-image", wrong, key); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong image accepted", err)
	}
	for i := 0; i < 4; i++ {
		if _, err := db.EnqueueInvocation(ctx, p, d.ApplicationID, "report", fmt.Sprintf("request-%d", i), in, key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.EnqueueInvocation(ctx, p, d.ApplicationID, "report", "owner-overflow", in, key); !errors.Is(err, ErrConflict) {
		t.Fatal("owner queue exceeded", err)
	}
	c, err := db.ClaimInvocation(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	defer c.Release()
	if _, err = db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", p.KeyID); err != nil {
		t.Fatal(err)
	}
	if err = c.Check(ctx); err == nil {
		t.Fatal("revoked key retained execution authority")
	}
	if err = c.Save(ctx, invocation.RuntimeState{Status: invocation.Cancelled}, nil); err != nil {
		t.Fatal(err)
	}
	if err = c.Cleaned(ctx); err != nil {
		t.Fatal(err)
	}
	c.Release()
	if _, err = db.Accept(ctx, admin, "demo", "development", d.Spec, 1, "change-revision"); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM job_invocations WHERE status='queued'").Scan(&queued); err != nil || queued != 0 {
		t.Fatal("old revision remained queued", err)
	}
}

func TestInvocationBusyApplicationCannotHideOtherWork(t *testing.T) {
	db, admin, p, d, in, key := invocationFixture(t)
	ctx := context.Background()
	for owner := 0; owner < 4; owner++ {
		in.OwnerScope = fmt.Sprintf("owner-%d", owner)
		for i := 0; i < 4; i++ {
			if _, err := db.EnqueueInvocation(ctx, p, d.ApplicationID, "report", fmt.Sprintf("request-%d-%d", owner, i), in, key); err != nil {
				t.Fatal(err)
			}
		}
	}
	first, err := db.ClaimInvocation(ctx)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	defer first.Release()
	app := d.Spec
	app.Name = "second-app"
	second, err := db.Accept(ctx, admin, "demo", "development", app, 0, "second-app-initial", app)
	if err != nil {
		t.Fatal(err)
	}
	deploy, err := db.Claim(ctx)
	if err != nil || deploy == nil {
		t.Fatal(err)
	}
	if err = deploy.Finish(ctx, "succeeded", "", nil); err != nil {
		t.Fatal(err)
	}
	deploy.Release()
	_, raw, err := db.CreateKey(ctx, admin, KeyInput{Name: "second-client", Project: "demo", Environment: "development", Application: app.Name, Permissions: []string{"jobs:invoke"}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.EnqueueInvocation(ctx, other, second.ApplicationID, "report", "second-request", in, key); err != nil {
		t.Fatal(err)
	}
	active, err := db.ClaimInvocation(ctx)
	if err != nil || active == nil {
		t.Fatal("busy application hid runnable work", err)
	}
	defer active.Release()
	if active.Record.ApplicationID != second.ApplicationID {
		t.Fatal("claimed wrong application")
	}
}

func TestInvocationUsesOneDatabaseConnectionUnderPoolPressure(t *testing.T) {
	db, _, p, d, in, key := invocationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := int32(1); i < db.Pool.Config().MaxConns; i++ {
		conn, err := db.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(conn.Release)
	}
	if _, err := db.EnqueueInvocation(ctx, p, d.ApplicationID, "report", "single-connection", in, key); err != nil {
		t.Fatal("enqueue required a nested connection", err)
	}
	c, err := db.ClaimInvocation(ctx)
	if err != nil || c == nil {
		t.Fatal("claim required a nested connection", err)
	}
	defer c.Release()
	if err = c.Check(ctx); err != nil {
		t.Fatal("authority check required a nested connection", err)
	}
	if err = c.Save(ctx, invocation.RuntimeState{Status: invocation.Failed, Message: "Fixture complete"}, nil); err != nil {
		t.Fatal("receipt save required a nested connection", err)
	}
	if err = c.Cleaned(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestInvocationHistoryCapPreservesIdempotency(t *testing.T) {
	db, _, p, d, in, key := invocationFixture(t)
	ctx := context.Background()
	r, err := db.EnqueueInvocation(ctx, p, d.ApplicationID, "report", "retained-original", in, key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(ctx, `INSERT INTO job_invocations(id,application_id,deployment_id,service,revision,image,correlation_id,identity_id,key_id,owner_hash,idempotency_key,input_hash,encrypted_input,input_bytes,source,status,finished_at)
 SELECT md5('history-fixture-'||g::text),application_id,deployment_id,service,revision,image,correlation_id,identity_id,key_id,owner_hash,'history-fixture-'||g::text,input_hash,''::bytea,0,source,'succeeded',now() FROM job_invocations CROSS JOIN generate_series(1,999) g WHERE id=$1`, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.EnqueueInvocation(ctx, p, d.ApplicationID, "report", "history-overflow", in, key); !errors.Is(err, ErrConflict) {
		t.Fatal("history cap exceeded", err)
	}
	replay, err := db.EnqueueInvocation(ctx, p, d.ApplicationID, "report", "retained-original", in, key)
	if err != nil || replay.ID != r.ID {
		t.Fatal("history cap broke idempotency", err)
	}
}
