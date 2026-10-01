package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

func oracleSwitchoverFixture(t *testing.T) (*Store, Principal, database.Resource, database.OracleSwitchoverReview) {
	t.Helper()
	s, p, d := databaseFixture(t)
	d.Spec.Engine, d.Spec.Version, d.Spec.Mode = "oracle", "19", "cluster"
	d.Spec.CPU, d.Spec.Memory, d.Spec.StorageGiB = "1", "4Gi", 10
	d.Spec.Replicas = 2
	d.Spec.Oracle = &database.OracleConfig{Edition: "enterprise", Image: "registry.example.com/oracle@sha256:" + strings.Repeat("a", 64), LicenseConfirmed: true}
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "create-oracle-role-fixture", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observation := database.Observation{Revision: 1, Status: "ready", ObservedAt: time.Now().UTC(), Primary: "primary-pod", TopologyFingerprint: "synthetic-development-topology"}
	if err = s.RecordDatabaseStep(ctx, op, observation, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	d, err = s.Database(ctx, p, d.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	plan := database.OracleSwitchoverReview{RequestID: NewID(), DatabaseID: d.ID, Project: d.Project, Environment: d.Environment, Revision: d.Revision, BrokerUID: "development-broker", TopologyFingerprint: observation.TopologyFingerprint, Primary: "primary-pod", Target: "standby-pod", TargetController: "database-1", TargetUniqueName: "HPDB1", MemberUIDs: []string{"member-one", "member-two", "member-three"}, ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	return s, p, d, plan
}

func TestOracleSwitchoverDurableReviewAndLeases(t *testing.T) {
	s, p, d, plan := oracleSwitchoverFixture(t)
	ctx := context.Background()
	review, err := s.SaveDatabaseReview(ctx, p, d, "switchover", plan, plan.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	application := p
	application.Application = "other"
	if _, err = s.AcceptOracleSwitchover(ctx, application, d.ID, d.Revision, review, "app-role-request"); err == nil {
		t.Fatal("application key accepted a switchover")
	}
	maintenance, err := s.ClaimDatabaseMaintenance(ctx, d.ID, d.Revision)
	if err != nil || maintenance == nil {
		t.Fatal("maintenance claim failed", err)
	}
	if _, err = s.AcceptOracleSwitchover(ctx, p, d.ID, d.Revision, review, "oracle-role-request"); !errors.Is(err, ErrConflict) {
		t.Fatal("switchover overlapped maintenance", err)
	}
	maintenance.Release()
	op, err := s.AcceptOracleSwitchover(ctx, p, d.ID, d.Revision, review, "oracle-role-request")
	if err != nil {
		t.Fatal(err)
	}
	if op.Revision != d.Revision || op.Switchover == nil || op.Switchover.RequestID != plan.RequestID || op.Review != nil {
		t.Fatal("switchover lost its typed payload or rewrote the configuration revision")
	}
	replay, err := s.AcceptOracleSwitchover(ctx, p, d.ID, d.Revision, review, "oracle-role-request")
	if err != nil || replay.ID != op.ID {
		t.Fatal("switchover replay created another operation", err)
	}
	if _, err = s.AcceptOracleSwitchover(ctx, p, d.ID, d.Revision, NewID(), "oracle-role-request"); !errors.Is(err, ErrConflict) {
		t.Fatal("idempotency key accepted a different review", err)
	}
	if _, err = s.AcceptOracleSwitchover(ctx, p, d.ID, d.Revision, review, "consume-review-again"); !errors.Is(err, ErrConflict) {
		t.Fatal("review accepted twice", err)
	}
	if claim, err := s.ClaimDatabaseMaintenance(ctx, d.ID, d.Revision); err != nil || claim != nil {
		t.Fatal("maintenance overlapped switchover", err)
	}
	claimed, err := s.ClaimDatabaseOperation(ctx)
	if err != nil || claimed.ID != op.ID {
		t.Fatal("switchover was not durable work", err)
	}
	if err = s.CheckDatabaseOperation(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	stale := claimed
	stale.Lease = NewID()
	if err = s.CheckDatabaseOperation(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatal("stale worker retained role authority", err)
	}
	if err = s.RecordDatabaseStep(ctx, claimed, d.Observation, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Database(ctx, p, d.ID, true)
	if err != nil || stored.Revision != d.Revision || !stored.Spec.Equal(d.Spec) {
		t.Fatal("role change rewrote immutable configuration", err)
	}
	plan.RequestID = NewID()
	review, err = s.SaveDatabaseReview(ctx, p, stored, "switchover", plan, plan.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptOracleSwitchover(ctx, p, stored.ID, stored.Revision, review, "second-role-request"); err != nil {
		t.Fatal("a later reviewed role change could not use the same configuration revision", err)
	}
}

func TestOracleSwitchoverRejectsExpiredAndChangedReviews(t *testing.T) {
	s, p, d, plan := oracleSwitchoverFixture(t)
	ctx := context.Background()
	changed := plan
	changed.Environment = "other"
	if _, err := s.SaveDatabaseReview(ctx, p, d, "switchover", changed, changed.ExpiresAt); !errors.Is(err, ErrInput) {
		t.Fatal("foreign review scope was stored", err)
	}
	changed = plan
	changed.TargetUniqueName = "HPDB0"
	if _, err := s.SaveDatabaseReview(ctx, p, d, "switchover", changed, changed.ExpiresAt); !errors.Is(err, ErrInput) {
		t.Fatal("mismatched native target was stored", err)
	}
	review, err := s.SaveDatabaseReview(ctx, p, d, "switchover", plan, plan.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE managed_database_reviews SET expires_at=now()-interval '1 second' WHERE id=$1", review); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptOracleSwitchover(ctx, p, d.ID, d.Revision, review, "expired-role-request"); !errors.Is(err, ErrConflict) {
		t.Fatal("expired review was consumed", err)
	}
}

func TestOracleSwitchoverRetryRetainsReviewedTarget(t *testing.T) {
	s, p, d, plan := oracleSwitchoverFixture(t)
	ctx := context.Background()
	review, err := s.SaveDatabaseReview(ctx, p, d, "switchover", plan, plan.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.AcceptOracleSwitchover(ctx, p, d.ID, d.Revision, review, "role-retry-fixture")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, claimed, d.Observation, "failed", "timeout", "Development fixture timed out."); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryOracleSwitchover(ctx, p, d.ID, op.ID, d.Revision+1); !errors.Is(err, ErrConflict) {
		t.Fatal("retry ignored configuration revision", err)
	}
	retry, err := s.RetryOracleSwitchover(ctx, p, d.ID, op.ID, d.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID != op.ID || retry.Switchover == nil || retry.Switchover.RequestID != plan.RequestID || retry.Switchover.TargetUniqueName != plan.TargetUniqueName || retry.Status != "queued" {
		t.Fatal("retry changed the approved role operation")
	}
	if replay, err := s.RetryOracleSwitchover(ctx, p, d.ID, op.ID, d.Revision); err != nil || replay.ID != retry.ID {
		t.Fatal("retry was not idempotent", err)
	}
}

func TestOracleSwitchoverTerminalFailuresRequireTheRightRecovery(t *testing.T) {
	for _, phase := range []string{"review", "switchover"} {
		t.Run(phase, func(t *testing.T) {
			s, p, d, plan := oracleSwitchoverFixture(t)
			ctx := context.Background()
			review, err := s.SaveDatabaseReview(ctx, p, d, "switchover", plan, plan.ExpiresAt)
			if err != nil {
				t.Fatal(err)
			}
			op, err := s.AcceptOracleSwitchover(ctx, p, d.ID, d.Revision, review, "terminal-role-fixture")
			if err != nil {
				t.Fatal(err)
			}
			claimed, err := s.ClaimDatabaseOperation(ctx)
			if err != nil || claimed.ID != op.ID {
				t.Fatal("role claim failed", err)
			}
			if err := s.RecordDatabaseStep(ctx, claimed, d.Observation, "failed", phase, "Development fixture failure."); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RetryOracleSwitchover(ctx, p, d.ID, op.ID, d.Revision); !errors.Is(err, ErrConflict) {
				t.Fatal("terminal role failure was blindly retried", err)
			}
			stored, err := s.Database(ctx, p, d.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			plan.RequestID = strings.Repeat("e", 32)
			next, err := s.SaveDatabaseReview(ctx, p, stored, "switchover", plan, plan.ExpiresAt)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.AcceptOracleSwitchover(ctx, p, d.ID, d.Revision, next, "terminal-role-new-review")
			if phase == "review" && err != nil {
				t.Fatal("a stale pre-start review could not be replaced", err)
			}
			if phase == "switchover" && !errors.Is(err, ErrConflict) {
				t.Fatal("a new review bypassed an unresolved broker failure", err)
			}
		})
	}
}
