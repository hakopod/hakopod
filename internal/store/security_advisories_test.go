package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/advisory"
)

func runningSecurityApp(t *testing.T, db *Store, p Principal, expected int64, key string) Deployment {
	t.Helper()
	ctx := context.Background()
	d, err := db.Accept(ctx, p, "demo", "development", emptyTestSpec(), expected, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded',resolved_spec=spec WHERE id=$1", d.ID); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSecurityAdvisoriesStore(t *testing.T) {
	db := isolatedDatabase(t)
	p := bootstrapPrincipal(t, db)
	ctx := context.Background()

	// Feed lease: exclusive, owner-only save, save releases.
	if ok, err := db.ClaimFeed(ctx, "a", time.Minute); err != nil || !ok {
		t.Fatalf("claim a: %v %v", ok, err)
	}
	if ok, _ := db.ClaimFeed(ctx, "b", time.Minute); ok {
		t.Fatal("second claim succeeded during lease")
	}
	now := time.Now().UTC().Truncate(time.Second)
	st := FeedState{Body: []byte("x"), ETag: "e1", Revision: 7, CheckedAt: &now, NextCheckAt: now.Add(-time.Second)}
	if err := db.SaveFeed(ctx, "b", st); !errors.Is(err, ErrConflict) {
		t.Fatalf("non-owner save: %v", err)
	}
	if err := db.SaveFeed(ctx, "a", st); err != nil {
		t.Fatal(err)
	}
	got, err := db.FeedState(ctx)
	if err != nil || got.Revision != 7 || got.ETag != "e1" || !got.CheckedAt.Equal(now) {
		t.Fatalf("feed state %+v %v", got, err)
	}
	if ok, _ := db.ClaimFeed(ctx, "b", time.Minute); !ok {
		t.Fatal("claim after save failed")
	}
	if ok, _ := db.ClaimAdvisoryScan(ctx, "a", time.Minute); !ok {
		t.Fatal("scan claim failed")
	}
	if ok, _ := db.ClaimAdvisoryScan(ctx, "b", time.Minute); ok {
		t.Fatal("scan claimed twice")
	}

	// OSV cache.
	if _, ok, err := db.OSVCached(ctx, "node", "22.3.0", time.Hour); ok || err != nil {
		t.Fatalf("empty cache hit %v %v", ok, err)
	}
	if err := db.SaveOSV(ctx, "node", "22.3.0", []byte(`[]`)); err != nil {
		t.Fatal(err)
	}
	if body, ok, err := db.OSVCached(ctx, "node", "22.3.0", time.Hour); !ok || err != nil || string(body) != "[]" {
		t.Fatalf("cache %s %v %v", body, ok, err)
	}

	// Candidates: an app with no succeeded deployment is not a candidate.
	pending, err := db.Accept(ctx, p, "demo", "development", emptyTestSpec(), 0, "security-pending")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := db.SecurityCandidates(ctx, 0, 0, false, 10); len(c) != 0 {
		t.Fatalf("pending app is a candidate: %+v", c)
	}
	db.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded',resolved_spec=spec WHERE id=$1", pending.ID)
	c, err := db.SecurityCandidates(ctx, 0, 0, false, 0)
	if err != nil || len(c) != 1 || c[0].DeploymentID != pending.ID || c[0].ApplicationID != pending.ApplicationID {
		t.Fatalf("new app candidate %+v %v", c, err)
	}
	appID := c[0].ApplicationID

	// Empty row reads as defaults.
	sec, err := db.ApplicationSecurity(ctx, appID)
	if err != nil || sec.PolicyRevision != 1 || sec.Findings == nil || sec.ApplicationName != "store-test" {
		t.Fatalf("default security %+v %v", sec, err)
	}

	// Save/read round trip removes candidacy.
	f := advisory.Finding{AdvisoryID: "HKP-1", AdvisoryRevision: 1, Severity: advisory.High, Service: "api", Fix: advisory.FixReresolve}
	sec.DeploymentID, sec.Revision, sec.FeedRevision, sec.EvaluatedAt, sec.CheckedAt = pending.ID, c[0].Revision, 7, &now, &now
	sec.Inventory = &advisory.Inventory{ApplicationID: appID, Services: []advisory.ServiceEvidence{{Service: "api", Kind: advisory.KindImage}}}
	sec.Findings = []advisory.Finding{f}
	if err = db.SaveApplicationSecurity(ctx, sec); err != nil {
		t.Fatal(err)
	}
	back, err := db.ApplicationSecurity(ctx, appID)
	if err != nil || len(back.Findings) != 1 || back.Findings[0].AdvisoryID != "HKP-1" || back.Inventory == nil || back.Inventory.Services[0].Service != "api" {
		t.Fatalf("round trip %+v %v", back, err)
	}
	if c, _ := db.SecurityCandidates(ctx, 7, 0, false, 10); len(c) != 0 {
		t.Fatalf("evaluated app is a candidate: %+v", c)
	}
	if c, _ := db.SecurityCandidates(ctx, 7, 24*time.Hour, true, 10); len(c) != 1 {
		t.Fatalf("never OSV-checked app must be due while OSV is enabled: %+v", c)
	}
	if c, _ := db.SecurityCandidates(ctx, 8, 0, false, 10); len(c) != 1 {
		t.Fatalf("feed revision bump did not select: %+v", c)
	}
	next := runningSecurityApp(t, db, p, c[0].Revision, "security-next")
	if c, _ := db.SecurityCandidates(ctx, 7, 0, false, 10); len(c) != 1 || c[0].DeploymentID != next.ID {
		t.Fatalf("changed deployment not selected: %+v", c)
	}

	// Policy concurrency.
	pol, err := db.SetSecurityPolicy(ctx, p, appID, true, 1)
	if err != nil || !pol.AutoPatch || pol.ApprovedBy != p.ID || pol.GrantID == "" || pol.PolicyRevision != 2 || len(pol.Findings) != 1 {
		t.Fatalf("policy %+v %v", pol, err)
	}
	if err = db.Reauthorize(ctx, pol.GrantID, pol.Project, pol.Environment, pol.ApplicationName); err != nil {
		t.Fatalf("grant reauthorize after enable: %v", err)
	}
	grant := pol.GrantID
	if _, err = db.SetSecurityPolicy(ctx, p, appID, false, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale policy: %v", err)
	}
	if pol, err = db.SetSecurityPolicy(ctx, p, appID, false, 2); err != nil || pol.AutoPatch || pol.ApprovedBy != "" || pol.GrantID != "" {
		t.Fatalf("disable %+v %v", pol, err)
	}
	if err = db.Reauthorize(ctx, grant, pol.Project, pol.Environment, pol.ApplicationName); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("grant reauthorize after disable: %v", err)
	}

	// Overview permission filtering.
	if rows, err := db.SecurityOverview(ctx, p); err != nil || len(rows) != 1 {
		t.Fatalf("overview %+v %v", rows, err)
	}
	// Incomplete checks surface even without findings or automatic patching.
	quiet, err := db.ApplicationSecurity(ctx, appID)
	if err != nil {
		t.Fatal(err)
	}
	quiet.Findings, quiet.Inventory = nil, &advisory.Inventory{Incomplete: []string{"OSV.dev could not be reached."}}
	if err = db.SaveApplicationSecurity(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if rows, err := db.SecurityOverview(ctx, p); err != nil || len(rows) != 1 {
		t.Fatalf("incomplete overview %+v %v", rows, err)
	}
	quiet.Inventory = &advisory.Inventory{}
	if err = db.SaveApplicationSecurity(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if rows, err := db.SecurityOverview(ctx, p); err != nil || len(rows) != 0 {
		t.Fatalf("complete quiet app listed %+v %v", rows, err)
	}
	if err = db.SaveApplicationSecurity(ctx, func() ApplicationSecurity {
		quiet.Inventory = &advisory.Inventory{Incomplete: []string{"x"}}
		return quiet
	}()); err != nil {
		t.Fatal(err)
	}
	scoped := p
	scoped.Application = quiet.ApplicationName
	if rows, err := db.SecurityOverview(ctx, scoped); err != nil || len(rows) != 1 {
		t.Fatalf("app-scoped overview %+v %v", rows, err)
	}
	scoped.Application = "another-app"
	if rows, err := db.SecurityOverview(ctx, scoped); err != nil || len(rows) != 0 {
		t.Fatalf("app-scoped overview leaked %+v %v", rows, err)
	}
	// A failed evaluation backs off and records why.
	due, _ := db.SecurityCandidates(ctx, 99, 0, false, 10)
	if len(due) != 1 {
		t.Fatalf("expected due app: %+v", due)
	}
	quiet.DeploymentID = due[0].DeploymentID
	if err = db.SaveApplicationSecurity(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if err = db.SecurityEvaluationFailed(ctx, appID, "Evaluation failed: timed out."); err != nil {
		t.Fatal(err)
	}
	if c, _ := db.SecurityCandidates(ctx, 99, 0, false, 10); len(c) != 0 {
		t.Fatalf("backing-off app selected: %+v", c)
	}
	if failedRow, err := db.ApplicationSecurity(ctx, appID); err != nil || failedRow.Inventory == nil || failedRow.Inventory.Incomplete[0] != "Evaluation failed: timed out." {
		t.Fatalf("failure row %+v %v", failedRow, err)
	}
	// A later failed rollout keeps the latest succeeded deployment as evidence.
	latest, err := db.LatestSucceededDeployment(ctx, appID)
	if err != nil {
		t.Fatal(err)
	}
	broken, err := db.Accept(ctx, p, "demo", "development", emptyTestSpec(), latest.Revision, "security-broken")
	if err != nil {
		t.Fatal(err)
	}
	db.Pool.Exec(ctx, "UPDATE deployments SET status='failed' WHERE id=$1", broken.ID)
	db.Pool.Exec(ctx, "UPDATE application_security SET next_evaluation_at=NULL WHERE application_id=$1", appID)
	if c, _ := db.SecurityCandidates(ctx, 99, 0, false, 10); len(c) != 1 || c[0].DeploymentID != latest.ID || c[0].Revision != broken.Revision {
		t.Fatalf("failed rollout froze evaluation: %+v", c)
	}
	// A new succeeded deployment is evaluated at once, even while backing off.
	if err = db.SecurityEvaluationFailed(ctx, appID, "Evaluation failed: timed out."); err != nil {
		t.Fatal(err)
	}
	if c, _ := db.SecurityCandidates(ctx, 99, 0, false, 10); len(c) != 0 {
		t.Fatalf("backing-off app selected: %+v", c)
	}
	fresh := runningSecurityApp(t, db, p, broken.Revision, "security-fresh")
	if c, _ := db.SecurityCandidates(ctx, 99, 0, false, 10); len(c) != 1 || c[0].DeploymentID != fresh.ID {
		t.Fatalf("new deployment waited for backoff: %+v", c)
	}
	other := p
	other.Project = "other"
	if rows, err := db.SecurityOverview(ctx, other); err != nil || len(rows) != 0 {
		t.Fatalf("overview leaked %+v %v", rows, err)
	}

	// Patch attempt dedupe.
	a := PatchAttempt{ID: "attempt-1", ApplicationID: appID, AdvisoryID: "HKP-1", AdvisoryRevision: 1, Service: "api", FromDeploymentID: pending.ID}
	if ok, err := db.ClaimPatchAttempt(ctx, a); !ok || err != nil {
		t.Fatalf("claim %v %v", ok, err)
	}
	if ok, err := db.ClaimPatchAttempt(ctx, a); ok || err != nil {
		t.Fatalf("duplicate claim %v %v", ok, err)
	}
	if open, _ := db.OpenPatchAttempts(ctx, 10); len(open) != 1 {
		t.Fatalf("open %+v", open)
	}
	if err = db.UpdatePatchAttempt(ctx, "attempt-1", "verified", next.ID, ""); err != nil {
		t.Fatal(err)
	}
	if open, _ := db.OpenPatchAttempts(ctx, 10); len(open) != 0 {
		t.Fatalf("still open %+v", open)
	}
	if list, _ := db.PatchAttempts(ctx, appID, 10); len(list) != 1 || list[0].DeploymentID != next.ID || list[0].Status != "verified" {
		t.Fatalf("attempts %+v", list)
	}

	// Settings defaults and conflict.
	v, rev, err := db.SecuritySettings(ctx)
	if err != nil || rev != 0 || !v.FeedEnabled || !v.OSVEnabled {
		t.Fatalf("settings default %+v %d %v", v, rev, err)
	}
	if rev, err = db.PutSecuritySettings(ctx, SecuritySettings{FeedEnabled: true}, 0); err != nil || rev != 1 {
		t.Fatalf("put %d %v", rev, err)
	}
	if _, err = db.PutSecuritySettings(ctx, SecuritySettings{}, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale put %v", err)
	}
	if v, rev, _ = db.SecuritySettings(ctx); rev != 1 || v.OSVEnabled {
		t.Fatalf("settings %+v %d", v, rev)
	}
	if rev, err = db.PutSecuritySettings(ctx, SecuritySettings{}, 1); err != nil || rev != 2 {
		t.Fatalf("update %d %v", rev, err)
	}
}
