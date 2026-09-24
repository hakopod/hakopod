package store

import (
	"context"
	"errors"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

func retainedFixture(t *testing.T, cleanup bool) (*Store, Principal, string) {
	t.Helper()
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	s.StorageBudget = func(context.Context, string, string) (int64, error) { return 15, nil }
	s.ValidateDeployment = func(context.Context, Application, spec.Application) error { return nil }
	app := emptyTestSpec()
	v := app.Services["api"]
	v.Volume = &spec.Volume{SizeGiB: 10, MountPath: "/data"}
	app.Services["api"] = v
	first, err := s.Accept(ctx, p, "demo", "development", app, 0, "retained-initial")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	app.Services = map[string]spec.Service{}
	last, err := s.Accept(ctx, p, "demo", "development", app, 1, "retained-empty")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", last.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteEmptyApplication(ctx, p, first.ApplicationID, 2, app.Name, cleanup); err != nil {
		t.Fatal(err)
	}
	return s, p, first.ApplicationID
}
func TestRetainedDataRequiresExplicitConsentAndVerifiedReclamation(t *testing.T) {
	s, p, id := retainedFixture(t, false)
	ctx := context.Background()
	items, err := s.RetainedApplications(ctx, p, "demo", "development")
	if err != nil || len(items) != 1 || items[0].Status != "retained" || items[0].ReservedGiB != 10 {
		t.Fatal(items, err)
	}
	if claim, err := s.ClaimRetainedCleanup(ctx); err != nil || claim != nil {
		t.Fatal("unrequested cleanup claimed", err)
	}
	if err = s.RequestRetainedCleanup(ctx, p, id, "wrong"); !errors.Is(err, ErrInput) {
		t.Fatal(err)
	}
	foreign := p
	foreign.Project = "other"
	if err = s.RequestRetainedCleanup(ctx, foreign, id, items[0].Name); !errors.Is(err, ErrForbidden) {
		t.Fatal("cross-scope cleanup", err)
	}
	if err = s.RequestRetainedCleanup(ctx, p, id, items[0].Name); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimRetainedCleanup(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if err = claim.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if second, err := s.ClaimRetainedCleanup(ctx); err != nil || second != nil {
		t.Fatal("duplicate cleanup claim", err)
	}
	if err = claim.Finish(ctx, "Disk provisioner is still reclaiming"); err != nil {
		t.Fatal(err)
	}
	claim.Release()
	var reserved int
	if err = s.Pool.QueryRow(ctx, "SELECT sum(size_gib) FROM storage_reservations WHERE application_id=$1", id).Scan(&reserved); err != nil || reserved != 10 {
		t.Fatal("quota released before disk reclamation", err)
	}
	claim, err = s.ClaimRetainedCleanup(ctx)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	defer claim.Release()
	if err = claim.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	items, err = s.RetainedApplications(ctx, p, "demo", "development")
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM storage_reservations WHERE application_id=$1", id).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatal("reclaimed quota stuck", err)
	}
}
func TestApplicationDeletionOptInQueuesCleanupAndRechecksAuthority(t *testing.T) {
	s, p, id := retainedFixture(t, true)
	ctx := context.Background()
	claim, err := s.ClaimRetainedCleanup(ctx)
	if err != nil || claim == nil || claim.Data.ApplicationID != id {
		t.Fatal(err)
	}
	defer claim.Release()
	s.AuthorizeRetainedCleanup = func(context.Context, Principal, string, string) error { return ErrForbidden }
	if err = claim.Check(ctx); !errors.Is(err, ErrForbidden) {
		t.Fatal("workspace policy was bypassed", err)
	}
	s.AuthorizeRetainedCleanup = nil
	if _, err = s.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", p.KeyID); err != nil {
		t.Fatal(err)
	}
	if err = claim.Check(ctx); err == nil {
		t.Fatal("revoked cleanup key retained authority")
	}
}
