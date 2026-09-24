package store

import (
	"context"
	"errors"
	"github.com/hakopod/hakopod/internal/spec"
	"testing"
)

func TestServiceVolumeConsentAndAccounting(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	s.StorageBudget = func(context.Context, string, string) (int64, error) { return 30, nil }
	s.ValidateDeployment = func(context.Context, Application, spec.Application) error { return nil }
	app := emptyTestSpec()
	v := app.Services["api"]
	v.Volume = &spec.Volume{MountPath: "/data", SizeGiB: 10}
	app.Services["api"] = v
	app.Services["web"] = v
	first, err := s.Accept(ctx, p, "demo", "development", app, 0, "service-volume-first")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	delete(app.Services, "api")
	removal, err := s.AcceptWithVolumeCleanup(ctx, p, "demo", "development", app, 1, "service-volume-remove", nil, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptWithVolumeCleanup(ctx, p, "demo", "development", app, 1, "service-volume-remove", nil, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("consent was absent from idempotency hash", err)
	}
	if c, err := s.ClaimServiceVolumeCleanup(ctx); err != nil || c != nil {
		t.Fatal("cleanup before rollout succeeded", err)
	}
	if _, err = s.Accept(ctx, p, "demo", "development", app, 2, "service-volume-race"); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted conflicting edit", err)
	}
	_, err = s.Pool.Exec(ctx, "UPDATE deployments SET status='succeeded' WHERE id=$1", removal.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimServiceVolumeCleanup(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if len(c.Claims) != 1 || c.Claims[0] != "api-data" {
		t.Fatal(c.Claims)
	}
	if err = c.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if other, err := s.ClaimServiceVolumeCleanup(ctx); err != nil || other != nil {
		t.Fatal("concurrent worker claim", err)
	}
	if err = c.Finish(ctx, "disk still exists"); err != nil {
		t.Fatal(err)
	}
	c.Release()
	var size int
	if err = s.Pool.QueryRow(ctx, "SELECT sum(size_gib) FROM storage_reservations WHERE application_id=$1", first.ApplicationID).Scan(&size); err != nil || size != 20 {
		t.Fatal("premature quota release", size, err)
	}
	c, err = s.ClaimServiceVolumeCleanup(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	defer c.Release()
	s.AuthorizeRetainedCleanup = func(context.Context, Principal, string, string) error { return ErrForbidden }
	if err = c.Check(ctx); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked Cloud membership ignored", err)
	}
	s.AuthorizeRetainedCleanup = nil
	if err = c.Finish(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT sum(size_gib) FROM storage_reservations WHERE application_id=$1", first.ApplicationID).Scan(&size); err != nil || size != 10 {
		t.Fatal("wrong remaining quota", size, err)
	}
	status, err := s.VolumeCleanup(ctx, removal.ID)
	if err != nil || status.Status != "deleted" {
		t.Fatal(status, err)
	}
}
func TestServiceVolumeClaimsKeepSharedMounts(t *testing.T) {
	old := spec.Application{Services: map[string]spec.Service{"db": {Volume: &spec.Volume{SizeGiB: 1}, Mounts: []spec.Mount{{Volume: "shared"}, {Volume: "private"}}}}, Volumes: map[string]spec.NamedVolume{}}
	next := spec.Application{Services: map[string]spec.Service{"web": {Mounts: []spec.Mount{{Volume: "shared"}}}}, Volumes: map[string]spec.NamedVolume{"shared": {SizeGiB: 1}}}
	claims, err := ServiceVolumeClaims(old, next, []string{"db"})
	if err != nil || len(claims) != 2 || claims[0] != "db-data" || claims[1] != "hakopod-volume-private" {
		t.Fatal(claims, err)
	}
	if _, err = ServiceVolumeClaims(old, old, []string{"db"}); err == nil {
		t.Fatal("active service allowed")
	}
	if _, err = ServiceVolumeClaims(old, next, []string{"foreign"}); err == nil {
		t.Fatal("unknown service allowed")
	}
}
