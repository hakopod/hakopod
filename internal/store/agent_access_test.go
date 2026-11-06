package store

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hakopod/hakopod/internal/agentaccess"
	"github.com/hakopod/hakopod/internal/license"
)

// agentPolicyLicense installs an in-memory Pro license that includes agent policies.
func agentPolicyLicense(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	db.LicenseVerifier = license.NewVerifier(map[string]ed25519.PublicKey{"ephemeral-fixture": public})
	status, err := db.LicenseStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute).Unix()
	payload, _ := json.Marshal(license.Claims{Version: 1, KeyID: "ephemeral-fixture", LicenseID: strings.Repeat("a", 32), InstallationID: status.InstallationID, Customer: "Local test fixture", Plan: "pro", Sequence: 1, IssuedAt: now, NotBefore: now, ExpiresAt: now + 7200, Features: []string{AgentPoliciesFeature}})
	signature := ed25519.Sign(private, append([]byte(license.Domain), payload...))
	token := "hl1." + base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature)
	digest := sha256.Sum256([]byte(token))
	if _, err = db.Pool.Exec(ctx, "UPDATE installation_license SET token=$1,token_digest=$2,highest_sequence=1,revision=1 WHERE singleton", token, digest[:]); err != nil {
		t.Fatal(err)
	}
}

func agentAdminPrincipal(t *testing.T, db *Store) Principal {
	p := bootstrapPrincipal(t, db)
	p.CredentialType = "browser"
	p.Email = "admin@example.test"
	return p
}

// agentHuman creates a non-admin browser identity.
func agentHuman(t *testing.T, db *Store, name string) Principal {
	t.Helper()
	id := NewID()
	if _, err := db.Pool.Exec(context.Background(), "INSERT INTO identities(id,name) VALUES($1,$2)", id, name); err != nil {
		t.Fatal(err)
	}
	return Principal{ID: id, Name: name, Email: name + "@example.test", CredentialType: "browser"}
}

func agentMachineKey(t *testing.T, db *Store, identityID string) string {
	t.Helper()
	id := NewID()
	if _, err := db.Pool.Exec(context.Background(), "INSERT INTO api_keys(id,identity_id,name,digest,prefix,permissions,expires_at) VALUES($1,$2,'agent',$3,$4,ARRAY['deployments:read'],now()+interval '1 day')", id, identityID, []byte(id), "hp_"+id[:8]); err != nil {
		t.Fatal(err)
	}
	return id
}

func blockDocument() agentaccess.Document {
	return agentaccess.Document{SchemaVersion: 1, When: []agentaccess.Condition{{Field: agentaccess.FieldOperation, Operator: agentaccess.OpIsAnyOf, Values: []string{"deleteApplication"}}}, Then: agentaccess.Then{Effect: agentaccess.EffectBlock}}
}

func TestAgentPolicyLifecycle(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	admin := agentAdminPrincipal(t, db)
	if _, err := db.CreateAgentPolicy(ctx, admin, "No deletes", blockDocument()); !errors.Is(err, ErrLicenseRequired) {
		t.Fatalf("unlicensed create err=%v", err)
	}
	agentPolicyLicense(t, db)
	other := agentHuman(t, db, "dev")
	if _, _, err := db.ListAgentPolicies(ctx, other); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin list err=%v", err)
	}
	if _, err := db.CreateAgentPolicy(ctx, other, "x", blockDocument()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin create err=%v", err)
	}
	if _, err := db.CreateAgentPolicy(ctx, admin, "", blockDocument()); !errors.Is(err, ErrInput) {
		t.Fatalf("empty name err=%v", err)
	}
	pol, err := db.CreateAgentPolicy(ctx, admin, "No deletes", blockDocument())
	if err != nil || pol.Status != "draft" || pol.Draft == nil || pol.Document != nil || pol.Revision != 1 {
		t.Fatalf("create %+v err=%v", pol, err)
	}
	if _, err = db.SaveAgentPolicyDraft(ctx, admin, pol.ID, 7, "No deletes", blockDocument()); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale draft err=%v", err)
	}
	if pol, err = db.SaveAgentPolicyDraft(ctx, admin, pol.ID, 1, "No app deletes", blockDocument()); err != nil || pol.Revision != 2 {
		t.Fatalf("draft %+v err=%v", pol, err)
	}
	if pol, err = db.PublishAgentPolicy(ctx, admin, pol.ID, 2, "monitor"); err != nil || pol.Status != "monitor" || pol.CurrentVersion != 1 || pol.Draft != nil || pol.Document == nil {
		t.Fatalf("publish %+v err=%v", pol, err)
	}
	// No draft: republishing switches monitor to enforced.
	if pol, err = db.PublishAgentPolicy(ctx, admin, pol.ID, pol.Revision, "enforced"); err != nil || pol.Status != "enforced" || pol.CurrentVersion != 2 {
		t.Fatalf("switch %+v err=%v", pol, err)
	}
	versions, err := db.AgentPolicyVersions(ctx, admin, pol.ID)
	if err != nil || len(versions) != 2 || versions[0].Version != 2 || versions[1].Status != "monitor" {
		t.Fatalf("versions %+v err=%v", versions, err)
	}
	published, err := db.PublishedAgentPolicies(ctx)
	if err != nil || len(published) != 1 || published[0].Version != 2 {
		t.Fatalf("published %+v err=%v", published, err)
	}
	list, sum, err := db.ListAgentPolicies(ctx, admin)
	if err != nil || len(list) != 1 || sum.Enforced != 1 || sum.Total != 1 {
		t.Fatalf("list %+v %+v err=%v", list, sum, err)
	}
	empty, _ := db.CreateAgentPolicy(ctx, admin, "Empty", blockDocument())
	if _, err = db.Pool.Exec(ctx, "UPDATE agent_access_policies SET draft=NULL WHERE id=$1", empty.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.PublishAgentPolicy(ctx, admin, empty.ID, empty.Revision, "enforced"); !errors.Is(err, ErrInput) {
		t.Fatalf("publish nothing err=%v", err)
	}
	if err = db.DeleteAgentPolicy(ctx, admin, pol.ID, pol.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AgentPolicy(ctx, admin, pol.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted policy err=%v", err)
	}
}

func TestAgentApprovalFlow(t *testing.T) {
	db := isolatedDatabase(t)
	ctx := context.Background()
	admin := agentAdminPrincipal(t, db)
	agentPolicyLicense(t, db)
	pol, err := db.CreateAgentPolicy(ctx, admin, "Hold deletes", blockDocument())
	if err != nil {
		t.Fatal(err)
	}
	if pol, err = db.PublishAgentPolicy(ctx, admin, pol.ID, pol.Revision, "enforced"); err != nil {
		t.Fatal(err)
	}
	requester := agentHuman(t, db, "requester")
	keyID := agentMachineKey(t, db, requester.ID)
	approver := agentHuman(t, db, "approver")
	teamMember := agentHuman(t, db, "teammate")
	if _, err = db.Pool.Exec(ctx, "INSERT INTO teams(id,name) VALUES('team-ops','ops')"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO team_members(team_id,identity_id,role) VALUES('team-ops',$1,'member')", teamMember.ID); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("DELETE /v1/applications/x"))
	in := AgentApprovalRequest{PolicyID: pol.ID, PolicyVersion: 1, KeyID: keyID, IdentityID: requester.ID, Operation: "deleteApplication", Category: "applications", Method: "DELETE", Path: "/v1/applications/x", Project: "demo", Environment: "development", Digest: digest, Approvers: []agentaccess.Approver{{Kind: "identity", ID: approver.ID}, {Kind: "team", ID: "team-ops"}}, TTL: time.Hour}
	first, created, err := db.RequestAgentApproval(ctx, in)
	if err != nil || !created || first.State != "pending" {
		t.Fatalf("request %+v err=%v", first, err)
	}
	again, created, err := db.RequestAgentApproval(ctx, in)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("dedupe %+v created=%v err=%v", again, created, err)
	}
	// Approver kinds.
	self := requester
	for _, c := range []struct {
		p    Principal
		want bool
	}{{approver, true}, {teamMember, true}, {admin, false}, {agentHuman(t, db, "stranger"), false}, {self, false}} {
		teams, _ := agentApproverTeams(ctx, db.Pool, c.p.ID, first)
		if got := canDecideAgentApproval(c.p, teams, first); got != c.want {
			t.Fatalf("canDecide %s = %v", c.p.Name, got)
		}
	}
	if !canDecideAgentApproval(admin, nil, AgentApproval{IdentityID: requester.ID}) {
		t.Fatal("admins decide approvals without approvers")
	}
	if canDecideAgentApproval(approver, nil, AgentApproval{IdentityID: requester.ID, Project: "demo", Approvers: []agentaccess.Approver{{Kind: "project_admins"}}}) {
		t.Fatal("non project admin decided")
	}
	if _, err = db.AgentApproval(ctx, agentHuman(t, db, "outsider"), first.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("outsider visibility err=%v", err)
	}
	if got, err := db.AgentApproval(ctx, Principal{ID: requester.ID, KeyID: keyID, CredentialType: "machine"}, first.ID); err != nil || got.ID != first.ID {
		t.Fatalf("requesting key visibility err=%v", err)
	}
	items, _, pending, err := db.AgentApprovals(ctx, approver, "pending", "", 10)
	if err != nil || len(items) != 1 || pending != 1 || !items[0].CanDecide {
		t.Fatalf("approver list %+v pending=%d err=%v", items, pending, err)
	}
	selfBrowser := requester
	if _, err = db.DecideAgentApproval(ctx, selfBrowser, first.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("self approval err=%v", err)
	}
	if _, err = db.ConsumeAgentApproval(ctx, first.ID, keyID, digest); !errors.Is(err, ErrForbidden) {
		t.Fatalf("consume pending err=%v", err)
	}
	decided, err := db.DecideAgentApproval(ctx, approver, first.ID, true, "looks right")
	if err != nil || decided.State != "approved" || decided.DecidedBy == nil || !decided.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("approve %+v err=%v", decided, err)
	}
	if _, err = db.ConsumeAgentApproval(ctx, first.ID, keyID, sha256.Sum256([]byte("other"))); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong digest err=%v", err)
	}
	if _, err = db.ConsumeAgentApproval(ctx, first.ID, agentMachineKey(t, db, requester.ID), digest); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong key err=%v", err)
	}
	if got, err := db.ConsumeAgentApproval(ctx, first.ID, keyID, digest); err != nil || got.State != "consumed" {
		t.Fatalf("consume %+v err=%v", got, err)
	}
	if _, err = db.ConsumeAgentApproval(ctx, first.ID, keyID, digest); !errors.Is(err, ErrForbidden) {
		t.Fatalf("second consume err=%v", err)
	}

	// Expired approvals cannot be decided or consumed, and do not block a new request.
	in.Digest = sha256.Sum256([]byte("second"))
	expired, _, err := db.RequestAgentApproval(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE agent_access_approvals SET expires_at=now()-interval '1 second' WHERE id=$1", expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.DecideAgentApproval(ctx, approver, expired.ID, true, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("decide expired err=%v", err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE agent_access_approvals SET state='approved' WHERE id=$1", expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ConsumeAgentApproval(ctx, expired.ID, keyID, in.Digest); !errors.Is(err, ErrForbidden) {
		t.Fatalf("consume expired err=%v", err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE agent_access_approvals SET state='pending' WHERE id=$1", expired.ID); err != nil {
		t.Fatal(err)
	}
	fresh, created, err := db.RequestAgentApproval(ctx, in)
	if err != nil || !created || fresh.ID == expired.ID {
		t.Fatalf("request after expiry %+v err=%v", fresh, err)
	}

	// One key may hold at most 20 open approvals; a repeat of an open one is
	// still reused.
	flood := in
	for i := 0; ; i++ {
		flood.Path = fmt.Sprintf("/flood/%d", i)
		flood.Digest = agentaccess.RequestDigest(keyID, "DELETE", flood.Path, "", nil)
		_, _, err = db.RequestAgentApproval(ctx, flood)
		if errors.Is(err, ErrApprovalLimit) {
			break
		}
		if err != nil || i > 25 {
			t.Fatalf("flood %d err=%v", i, err)
		}
	}
	if _, created, err := db.RequestAgentApproval(ctx, in); err != nil || created {
		t.Fatalf("reuse at limit created=%v err=%v", created, err)
	}
	gone := in
	gone.PolicyID, gone.Digest = "pol_missing", agentaccess.RequestDigest(keyID, "GET", "/gone", "", nil)
	if _, err = db.Pool.Exec(ctx, "UPDATE agent_access_approvals SET state='expired' WHERE key_id=$1 AND path LIKE '/flood/%'", keyID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.RequestAgentApproval(ctx, gone); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted policy err=%v", err)
	}

	// Prune expires pending rows and drops old history.
	if err = db.RecordAgentAccessEvaluations(ctx, []AgentAccessEvaluation{{ObservedAt: time.Now().Add(-40 * 24 * time.Hour), KeyID: keyID, IdentityID: requester.ID, Operation: "x", Category: "y", Decision: "allow"}, {KeyID: keyID, IdentityID: requester.ID, Operation: "x", Category: "y", Decision: "block", PolicyID: pol.ID, PolicyVersion: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE agent_access_approvals SET created_at=now()-interval '40 days' WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.PruneAgentAccess(ctx, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	evals, err := db.AgentAccessEvaluations(ctx, admin, time.Now().Add(-90*24*time.Hour), 100)
	if err != nil || len(evals) != 1 || evals[0].Decision != "block" || evals[0].KeyName != "agent" {
		t.Fatalf("evaluations %+v err=%v", evals, err)
	}
	var state string
	if err = db.Pool.QueryRow(ctx, "SELECT state FROM agent_access_approvals WHERE id=$1", fresh.ID).Scan(&state); err != nil || state != "expired" {
		t.Fatalf("pruned pending state=%q err=%v", state, err)
	}
	if _, err = db.AgentApproval(ctx, admin, first.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("old approval not pruned err=%v", err)
	}
}
