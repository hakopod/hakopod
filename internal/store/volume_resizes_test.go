package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

func resizeFixture(t *testing.T) (*Store, Principal, Application) {
	t.Helper()
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	s.StorageBudget = func(context.Context, string, string) (int64, error) { return 10, nil }
	s.ValidateDeployment = func(context.Context, Application, spec.Application) error { return nil }
	a := emptyTestSpec()
	v := a.Services["api"]
	v.Image = "nginx@sha256:" + strings.Repeat("a", 64)
	v.Volume = &spec.Volume{MountPath: "/data", SizeGiB: 10}
	a.Services["api"] = v
	a, err := spec.Normalize(a)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Accept(ctx, p, "demo", "development", a, 0, "resize-fixture", a)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Claim(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if err = c.Finish(ctx, "succeeded", "", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	c.Release()
	aStored, err := s.Application(ctx, d.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	return s, p, aStored
}

func TestVolumeResizeDurabilityQuotaAndAuthority(t *testing.T) {
	s, p, a := resizeFixture(t)
	ctx := context.Background()
	r, err := s.StartVolumeResize(ctx, p, a.ID, "api-data", 5, a.Revision, "resize-down", json.RawMessage(`{"source_uid":"source"}`))
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.StartVolumeResize(ctx, p, a.ID, "api-data", 5, a.Revision, "resize-down", nil)
	if err != nil || again.ID != r.ID {
		t.Fatal("idempotency", err)
	}
	if _, err = s.StartVolumeResize(ctx, p, a.ID, "api-data", 4, a.Revision, "resize-down", nil); !errors.Is(err, ErrConflict) {
		t.Fatal("changed input replay", err)
	}
	var quota int64
	if err = s.Pool.QueryRow(ctx, "SELECT sum(size_gib) FROM storage_reservations WHERE application_id=$1", a.ID).Scan(&quota); err != nil || quota != 15 {
		t.Fatal("temporary capacity not charged", quota, err)
	}
	if _, err = s.Accept(ctx, p, a.Project, a.Environment, a.Spec, a.Revision, "during-resize"); !errors.Is(err, ErrConflict) {
		t.Fatal("deployment raced maintenance", err)
	}
	c, err := s.ClaimVolumeResize(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if c2, err := s.ClaimVolumeResize(ctx); err != nil || c2 != nil {
		t.Fatal("parallel claim", err)
	}
	if err = c.BeginCutover(ctx); !errors.Is(err, ErrConflict) {
		t.Fatal("unverified cutover", err)
	}
	if err = c.Save(ctx, "copying", map[string]any{"verified": true, "bytes": 42}, ""); err != nil {
		t.Fatal(err)
	}
	c.Release()
	c, err = s.ClaimVolumeResize(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if !c.Resize.Verified || c.Resize.CopiedBytes != 42 {
		t.Fatal("verification lost after restart")
	}
	s.AuthorizeRetainedCleanup = func(context.Context, Principal, string, string) error { return ErrForbidden }
	if err = c.Check(ctx); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked membership ignored", err)
	}
	if err = c.BeginCutover(ctx); !errors.Is(err, ErrForbidden) {
		t.Fatal("cutover ignored revoked authority", err)
	}
	s.AuthorizeRetainedCleanup = nil
	if err = c.BeginCutover(ctx); err != nil {
		t.Fatal(err)
	}
	if ordinary, err := s.Claim(ctx); err != nil || ordinary != nil {
		t.Fatal("normal worker took migration", err)
	}
	if err = c.CutoverResult(ctx, map[string]any{"status": "healthy"}, ""); err != nil {
		t.Fatal(err)
	}
	c.Release()
	current, err := s.Application(ctx, a.ID)
	if err != nil || current.Revision != 2 || current.Spec.Services["api"].Volume != nil {
		t.Fatal("new revision not committed", err)
	}
	r, err = s.VolumeResize(ctx, r.ID)
	if err != nil || r.Phase != "original_retained" {
		t.Fatal(r, err)
	}
	if err = s.ResizeAction(ctx, p, a.ID, r.ID, "delete-original", "wrong"); !errors.Is(err, ErrConflict) {
		t.Fatal("deletion lacked exact confirmation", err)
	}
	if err = s.ResizeAction(ctx, p, a.ID, r.ID, "delete-original", r.Claim); err != nil {
		t.Fatal(err)
	}
	c, err = s.ClaimVolumeResize(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if err = c.Reclaimed(ctx, r.Claim, "completed"); err != nil {
		t.Fatal(err)
	}
	c.Release()
	if _, err = s.Accept(ctx, p, a.Project, a.Environment, a.Spec, current.Revision, "stale-git-original"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale Git recreated deleted original", err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT sum(size_gib) FROM storage_reservations WHERE application_id=$1", a.ID).Scan(&quota); err != nil || quota != 5 {
		t.Fatal("incorrect quota after reclamation", quota, err)
	}
}

func TestVolumeResizeCancellationKeepsOriginalAndPreventsGrowthOverQuota(t *testing.T) {
	s, p, a := resizeFixture(t)
	ctx := context.Background()
	if _, err := s.StartVolumeResize(ctx, p, a.ID, "api-data", 11, 1, "over-limit", nil); err == nil {
		t.Fatal("growth exceeded final quota")
	}
	r, err := s.StartVolumeResize(ctx, p, a.ID, "api-data", 4, 1, "cancel-resize", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ResizeAction(ctx, p, a.ID, r.ID, "cancel", ""); err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimVolumeResize(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if c.Resize.Phase != "cancelling" {
		t.Fatal(c.Resize.Phase)
	}
	if err = c.Reclaimed(ctx, r.TargetClaim, "cancelled"); err != nil {
		t.Fatal(err)
	}
	c.Release()
	current, err := s.Application(ctx, a.ID)
	if err != nil || current.Revision != 1 || current.Spec.Services["api"].Volume.SizeGiB != 10 {
		t.Fatal("cancel changed original", err)
	}
	var quota int64
	if err = s.Pool.QueryRow(ctx, "SELECT sum(size_gib) FROM storage_reservations WHERE application_id=$1", a.ID).Scan(&quota); err != nil || quota != 10 {
		t.Fatal("original quota lost", quota, err)
	}
}

func TestVolumeResizeDoesNotRaceRuntimeMaintenance(t *testing.T) {
	s, p, a := resizeFixture(t)
	ctx := context.Background()
	runtime, err := s.ClaimRuntime(ctx, a.ID, a.Revision)
	if err != nil || runtime == nil {
		t.Fatal(err)
	}
	defer runtime.Release()
	if _, err = s.StartVolumeResize(ctx, p, a.ID, "api-data", 5, a.Revision, "resize-runtime", JSON(map[string]string{"source_uid": "source"})); !errors.Is(err, ErrConflict) {
		t.Fatal("maintenance race", err)
	}
}

func TestVolumeResizeRetainedOriginalAllowsRepairWithoutMoreStorage(t *testing.T) {
	s, p, a := resizeFixture(t)
	ctx := context.Background()
	r, err := s.StartVolumeResize(ctx, p, a.ID, "api-data", 5, a.Revision, "resize-repair", JSON(map[string]string{"source_uid": "source"}))
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimVolumeResize(ctx)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if err = c.Save(ctx, "copying", map[string]any{"verified": true}, ""); err != nil {
		t.Fatal(err)
	}
	if err = c.BeginCutover(ctx); err != nil {
		t.Fatal(err)
	}
	if err = c.CutoverResult(ctx, map[string]any{}, "startup failed"); err != nil {
		t.Fatal(err)
	}
	c.Release()
	if err = s.ResizeAction(ctx, p, a.ID, r.ID, "retain", ""); err != nil {
		t.Fatal(err)
	}
	current, err := s.Application(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Accept(ctx, p, a.Project, a.Environment, current.Spec, current.Revision, "repair-same-storage"); err != nil {
		t.Fatal("retained original prevented repair", err)
	}
	var total int64
	if err = s.Pool.QueryRow(ctx, "SELECT sum(size_gib) FROM storage_reservations WHERE application_id=$1", a.ID).Scan(&total); err != nil || total != 15 {
		t.Fatal("repair discounted original", total, err)
	}
	current, err = s.Application(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Spec.Volumes["extra"] = spec.NamedVolume{SizeGiB: 1, AccessMode: "ReadWriteOnce"}
	if _, err = s.Accept(ctx, p, a.Project, a.Environment, current.Spec, current.Revision, "repair-extra-storage"); err == nil {
		t.Fatal("repair allowed additional storage above quota")
	}
}
