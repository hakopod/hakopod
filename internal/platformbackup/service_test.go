package platformbackup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type recoveryRepo struct {
	op             Operation
	finished       string
	heartbeatErr   error
	reauthorizeErr error
	cancelled      bool
}

func (r *recoveryRepo) ClaimPlatformRecovery(context.Context, string) (Operation, error) {
	r.op.Lease = "lease"
	return r.op, nil
}
func (r *recoveryRepo) ReauthorizePlatformRecovery(context.Context, Operation) error {
	return r.reauthorizeErr
}
func (r *recoveryRepo) HeartbeatPlatformRecovery(context.Context, Operation) (bool, error) {
	return r.cancelled, r.heartbeatErr
}
func (r *recoveryRepo) StepPlatformRecovery(context.Context, Operation, string, string) error {
	return nil
}
func (r *recoveryRepo) FinishPlatformRecovery(_ context.Context, _ Operation, status, _ string) error {
	r.finished = status
	return nil
}
func (r *recoveryRepo) SetPlatformRecoveryCleanup(_ context.Context, _ Operation, required bool) error {
	r.op.CleanupRequired = required
	return nil
}

type recoveryRuntime struct {
	events         []string
	manifest       Manifest
	parts          []CapturedPart
	drift          bool
	resumed        bool
	pauseErr       error
	cleanupContext bool
}

func (r *recoveryRuntime) ResolveSource(context.Context, Operation) (Manifest, error) {
	return r.manifest, nil
}
func (r *recoveryRuntime) ResolveEmptyTarget(context.Context, Operation, Manifest) error {
	r.events = append(r.events, "admit")
	return nil
}
func (r *recoveryRuntime) PauseWrites(context.Context, Operation) error {
	r.events = append(r.events, "pause")
	return r.pauseErr
}
func (r *recoveryRuntime) DrainWrites(context.Context, Operation) error {
	r.events = append(r.events, "drain")
	return nil
}
func (r *recoveryRuntime) Capture(context.Context, Operation, Manifest) ([]CapturedPart, map[string]string, error) {
	return r.parts, r.manifest.Verification, nil
}
func (r *recoveryRuntime) ReobserveSourceClaims(context.Context, Operation, Manifest) error {
	if r.drift {
		return errors.New("uid drift")
	}
	return nil
}
func (r *recoveryRuntime) ResumeSource(ctx context.Context, _ Operation) error {
	r.resumed = true
	r.cleanupContext = RecoveryCleanupFromContext(ctx)
	return nil
}
func (r *recoveryRuntime) RestorePart(_ context.Context, _ Operation, _ Manifest, p Part, input io.Reader) error {
	if len(r.events) == 0 {
		return errors.New("part before admission")
	}
	_, _ = io.Copy(io.Discard, input)
	r.events = append(r.events, p.Name)
	return nil
}
func (r *recoveryRuntime) VerifyRestoredContent(context.Context, Operation, Manifest) error {
	r.events = append(r.events, "content")
	return nil
}
func (r *recoveryRuntime) VerifyRestoredRuntime(context.Context, Operation, Manifest) error {
	r.events = append(r.events, "runtime")
	return nil
}

type recoveryArtifacts struct {
	archive   []byte
	published bool
	put       bool
}

func (a *recoveryArtifacts) ReservePlatformRecoveryUpload(context.Context, Operation, string) error {
	return nil
}
func (a *recoveryArtifacts) PutEncrypted(_ context.Context, _ Operation, _ Manifest, produce func(io.Writer) error) (string, error) {
	a.put = true
	var out bytes.Buffer
	if err := produce(&out); err != nil {
		return "", err
	}
	a.archive = out.Bytes()
	return strings.Repeat("e", 32), nil
}
func (a *recoveryArtifacts) OpenDecrypted(context.Context, string) (io.ReadCloser, error) {
	return readClose{bytes.NewReader(a.archive)}, nil
}
func (a *recoveryArtifacts) Publish(context.Context, Operation, Manifest, string) error {
	a.published = true
	return nil
}
func (a *recoveryArtifacts) Discard(context.Context, string) error { return nil }

func capturedFixture() (Manifest, []CapturedPart, []byte) {
	manifest, data := fixtureManifest()
	parts := []CapturedPart{}
	for _, part := range manifest.Parts {
		value := append([]byte(nil), data[part.Name]...)
		parts = append(parts, CapturedPart{Part: part, Open: func() (io.ReadCloser, error) { return readClose{bytes.NewReader(value)}, nil }})
	}
	manifest.Parts = nil
	return manifest, parts, nil
}

func TestRestoreAdmitsBeforeEncryptionClaimAndLeavesTargetIsolated(t *testing.T) {
	manifest, data := fixtureManifest()
	var archive bytes.Buffer
	if err := WriteArchive(&archive, manifest, func(p Part) (io.ReadCloser, error) { return readClose{bytes.NewReader(data[p.Name])}, nil }); err != nil {
		t.Fatal(err)
	}
	repo := &recoveryRepo{op: Operation{ID: strings.Repeat("1", 32), Kind: "restore", ArtifactID: strings.Repeat("2", 32)}}
	runtime := &recoveryRuntime{}
	artifacts := &recoveryArtifacts{archive: archive.Bytes()}
	service := Service{Repo: repo, Runtime: runtime, Artifacts: artifacts}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runtime.events) < 3 || runtime.events[0] != "admit" || runtime.events[1] != "database-encryption.tar" || runtime.events[len(runtime.events)-1] != "runtime" || repo.finished != "succeeded" {
		t.Fatalf("unsafe restore order: %#v status=%s", runtime.events, repo.finished)
	}
}

func TestBackupUIDDriftNeverPublishesAndResumesSource(t *testing.T) {
	manifest, parts, _ := capturedFixture()
	repo := &recoveryRepo{op: Operation{ID: strings.Repeat("1", 32), Kind: "backup"}}
	runtime := &recoveryRuntime{manifest: manifest, parts: parts, drift: true}
	artifacts := &recoveryArtifacts{}
	service := Service{Repo: repo, Runtime: runtime, Artifacts: artifacts}
	if err := service.RunOnce(context.Background()); err == nil {
		t.Fatal("UID drift accepted")
	}
	if artifacts.put || artifacts.published || !runtime.resumed || repo.finished != "failed" {
		t.Fatalf("unsafe drift handling: put=%v publish=%v resumed=%v status=%s", artifacts.put, artifacts.published, runtime.resumed, repo.finished)
	}
}

func TestPartialPauseFailureStillRunsAuthorizedCleanup(t *testing.T) {
	manifest, _, _ := capturedFixture()
	repo := &recoveryRepo{op: Operation{ID: strings.Repeat("1", 32), Kind: "backup"}}
	runtime := &recoveryRuntime{manifest: manifest, pauseErr: errors.New("partial pause")}
	service := Service{Repo: repo, Runtime: runtime, Artifacts: &recoveryArtifacts{}}
	if err := service.RunOnce(context.Background()); err == nil {
		t.Fatal("partial pause failure accepted")
	}
	if !runtime.resumed || !runtime.cleanupContext || repo.finished != "failed" {
		t.Fatalf("cleanup missing: resumed=%v cleanup_context=%v status=%s", runtime.resumed, runtime.cleanupContext, repo.finished)
	}
}

func TestCancelledBeforePauseDoesNotMutateRuntime(t *testing.T) {
	manifest, _, _ := capturedFixture()
	repo := &recoveryRepo{op: Operation{ID: strings.Repeat("1", 32), Kind: "backup"}, cancelled: true}
	runtime := &recoveryRuntime{manifest: manifest}
	service := Service{Repo: repo, Runtime: runtime, Artifacts: &recoveryArtifacts{}}
	if err := service.RunOnce(context.Background()); err == nil {
		t.Fatal("cancelled operation accepted")
	}
	if len(runtime.events) != 0 || runtime.resumed || repo.finished != "cancelled" {
		t.Fatalf("cancelled operation mutated runtime: events=%v resumed=%v status=%s", runtime.events, runtime.resumed, repo.finished)
	}
}

func TestDurableCleanupRunsAfterAuthorityRevocation(t *testing.T) {
	repo := &recoveryRepo{op: Operation{ID: strings.Repeat("1", 32), Kind: "backup", CleanupRequired: true}, reauthorizeErr: errors.New("revoked")}
	runtime := &recoveryRuntime{}
	service := Service{Repo: repo, Runtime: runtime, Artifacts: &recoveryArtifacts{}}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !runtime.resumed || !runtime.cleanupContext || repo.op.CleanupRequired || repo.finished != "cancelled" {
		t.Fatalf("durable cleanup did not finish safely: resumed=%v cleanup_context=%v required=%v status=%s", runtime.resumed, runtime.cleanupContext, repo.op.CleanupRequired, repo.finished)
	}
}
