package platformbackup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type recoveryRepo struct {
	op             Operation
	finished       string
	heartbeatErr   error
	reauthorizeErr error
	cancelled      bool
	cleanupChanges []bool
	finishCleanup  bool
	cleanupFence   func(context.Context, Operation) error
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
func (r *recoveryRepo) FinishPlatformRecovery(ctx context.Context, _ Operation, status, _ string) error {
	r.finished = status
	r.finishCleanup = RecoveryCleanupFromContext(ctx)
	return nil
}
func (r *recoveryRepo) SetPlatformRecoveryCleanup(_ context.Context, _ Operation, required bool) error {
	r.op.CleanupRequired = required
	r.cleanupChanges = append(r.cleanupChanges, required)
	return nil
}
func (r *recoveryRepo) FencePlatformRecoveryCleanup(ctx context.Context, op Operation) error {
	if r.cleanupFence != nil {
		return r.cleanupFence(ctx, op)
	}
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
	restoreCleaned bool
	preflightErr   error
	admitErr       error
	restoreErr     error
	cleanupErr     error
	cleanupOp      Operation
}

func (r *recoveryRuntime) ResolveSource(context.Context, Operation) (Manifest, error) {
	return r.manifest, nil
}
func (r *recoveryRuntime) PreflightRestoreTarget(context.Context, Operation, Manifest) error {
	return r.preflightErr
}
func (r *recoveryRuntime) ResolveEmptyTarget(context.Context, Operation, Manifest) error {
	r.events = append(r.events, "admit")
	return r.admitErr
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
func (r *recoveryRuntime) CleanupRestore(ctx context.Context, _ Operation) error {
	r.restoreCleaned = true
	r.cleanupContext = RecoveryCleanupFromContext(ctx)
	r.cleanupOp, _ = RecoveryOperationFromContext(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return r.cleanupErr
}
func (r *recoveryRuntime) RestorePart(_ context.Context, _ Operation, _ Manifest, p Part, input io.Reader) error {
	if len(r.events) == 0 {
		return errors.New("part before admission")
	}
	_, _ = io.Copy(io.Discard, input)
	r.events = append(r.events, p.Name)
	return r.restoreErr
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

func TestCancelledAdmittedRestoreRunsCleanupBeforeTerminalState(t *testing.T) {
	repo := &recoveryRepo{op: Operation{ID: strings.Repeat("1", 32), Kind: "restore", Phase: "target-admitted", CancelRequested: true, CleanupRequired: true}, reauthorizeErr: errors.New("revoked")}
	runtime := &recoveryRuntime{}
	service := Service{Repo: repo, Runtime: runtime, Artifacts: &recoveryArtifacts{}}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !runtime.restoreCleaned || !runtime.cleanupContext || repo.op.CleanupRequired || !repo.finishCleanup || runtime.cleanupOp.ID != repo.op.ID || repo.finished != "cancelled" {
		t.Fatalf("restore cleanup was not completed before terminal state: cleaned=%v context=%v status=%s", runtime.restoreCleaned, runtime.cleanupContext, repo.finished)
	}
}

func neonServiceArchive(t *testing.T) []byte {
	t.Helper()
	manifest, _ := fixtureManifest()
	manifest.Format = NeonFormat
	manifest.PlatformSpec = json.RawMessage(`{"kind":"neon"}`)
	manifest.PVCs = nil
	manifest.Neon = &NeonIdentity{TenantID: strings.Repeat("1", 32), TimelineID: strings.Repeat("2", 32), TenantGeneration: 3, TimelineGeneration: 4, CommitLSN: "0/16B6C50", PageserverRemoteConsistentLSNs: map[string]string{"0": "0/16B6C50", "1": "0/16B6C50"}, SourceObjectPrefix: "source-platform", ObjectInventorySHA256: strings.Repeat("3", 64), ObjectCount: 3, ObjectBytes: 1024}
	manifest.Parts = nil
	manifest.Verification = map[string]string{}
	for _, key := range []string{"tenant_identity", "tenant_generation", "timeline_identity", "timeline_generation", "remote_storage"} {
		manifest.Verification[key] = strings.Repeat("e", 64)
	}
	content := map[string][]byte{}
	for _, name := range NeonRequiredParts {
		data := []byte("content-" + name)
		sum := sha256.Sum256(data)
		content[name] = data
		manifest.Parts = append(manifest.Parts, Part{Name: name, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))})
	}
	var archive bytes.Buffer
	if err := WriteArchive(&archive, manifest, func(p Part) (io.ReadCloser, error) { return readClose{bytes.NewReader(content[p.Name])}, nil }); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestNeonManifestRequiresEveryPageserverToReachNonzeroCommit(t *testing.T) {
	archive := neonServiceArchive(t)
	manifest, err := ReadArchive(bytes.NewReader(archive), func(_ Part, input io.Reader) error { _, err := io.Copy(io.Discard, input); return err })
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestSHA256 = ""
	manifest.Neon.PageserverRemoteConsistentLSNs["1"] = "0/1"
	if manifest.Validate() == nil {
		t.Fatal("pageserver behind the artifact commit LSN accepted")
	}
	manifest.Neon.PageserverRemoteConsistentLSNs["1"] = manifest.Neon.CommitLSN
	manifest.Neon.CommitLSN = "0/0"
	if manifest.Validate() == nil {
		t.Fatal("zero recovery commit LSN accepted")
	}
}

func TestNeonRestorePreflightConflictDoesNotFlagOrMutateTarget(t *testing.T) {
	conflict := errors.New("fresh target required")
	repo := &recoveryRepo{op: Operation{ID: strings.Repeat("1", 32), Kind: "restore"}}
	runtime := &recoveryRuntime{preflightErr: conflict}
	service := Service{Repo: repo, Runtime: runtime, Artifacts: &recoveryArtifacts{archive: neonServiceArchive(t)}}
	if err := service.RunOnce(context.Background()); !errors.Is(err, conflict) {
		t.Fatalf("missing preflight error: %v", err)
	}
	if len(repo.cleanupChanges) != 0 || len(runtime.events) != 0 || runtime.restoreCleaned || repo.finished != "failed" {
		t.Fatalf("preflight conflict changed target: changes=%v events=%v cleaned=%v status=%s", repo.cleanupChanges, runtime.events, runtime.restoreCleaned, repo.finished)
	}
}

func TestNeonRestoreErrorsCleanupBeforeTerminalization(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "cancelled"}[cancelled], func(t *testing.T) {
			failure := errors.New("restore failed")
			if cancelled {
				failure = context.Canceled
			}
			repo := &recoveryRepo{op: Operation{ID: strings.Repeat("1", 32), Kind: "restore"}}
			runtime := &recoveryRuntime{restoreErr: failure}
			service := Service{Repo: repo, Runtime: runtime, Artifacts: &recoveryArtifacts{archive: neonServiceArchive(t)}}
			err := service.RunOnce(context.Background())
			if !errors.Is(err, failure) || errors.Is(err, errRecoveryCleanupRequired) {
				t.Fatalf("incorrect cleaned failure: %v", err)
			}
			if !runtime.restoreCleaned || !runtime.cleanupContext || runtime.cleanupOp.ID != repo.op.ID || repo.op.CleanupRequired || !repo.finishCleanup || repo.finished != map[bool]string{false: "failed", true: "cancelled"}[cancelled] {
				t.Fatalf("restore failure terminalized without cleanup: cleaned=%v flag=%v status=%s", runtime.restoreCleaned, repo.op.CleanupRequired, repo.finished)
			}
		})
	}
}

func TestNeonRestoreCleanupFailureRetainsDurableWork(t *testing.T) {
	repo := &recoveryRepo{op: Operation{ID: strings.Repeat("1", 32), Kind: "restore"}}
	failure := errors.New("cleanup failed")
	runtime := &recoveryRuntime{admitErr: errors.New("partial admission"), cleanupErr: failure}
	service := Service{Repo: repo, Runtime: runtime, Artifacts: &recoveryArtifacts{archive: neonServiceArchive(t)}}
	if err := service.RunOnce(context.Background()); !errors.Is(err, failure) || !errors.Is(err, errRecoveryCleanupRequired) {
		t.Fatalf("cleanup failure lost: %v", err)
	}
	if !repo.op.CleanupRequired || repo.finished != "" {
		t.Fatalf("pending cleanup terminalized: flag=%v status=%s", repo.op.CleanupRequired, repo.finished)
	}
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

func TestRecoveryCleanupRenewsLeaseUntilFlagIsCleared(t *testing.T) {
	op := Operation{ID: strings.Repeat("1", 32), Kind: "restore", Lease: "exact-lease"}
	renewed := make(chan struct{}, 1)
	var calls atomic.Int32
	repo := &recoveryRepo{op: op, cleanupFence: func(ctx context.Context, supplied Operation) error {
		if !RecoveryCleanupFromContext(ctx) || supplied.ID != op.ID || supplied.Kind != op.Kind || supplied.Lease != op.Lease {
			return errors.New("cleanup operation or context changed")
		}
		if calls.Add(1) == 2 {
			renewed <- struct{}{}
		}
		return nil
	}}
	service := Service{Repo: repo}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	err := service.cleanupWithInterval(parent, op, time.Second, time.Millisecond, func(ctx context.Context, _ Operation) error {
		select {
		case <-renewed:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil || calls.Load() < 3 || repo.op.CleanupRequired || len(repo.cleanupChanges) != 1 {
		t.Fatalf("cleanup did not renew before clearing flag: %v calls=%d changes=%v", err, calls.Load(), repo.cleanupChanges)
	}
}

func TestRecoveryCleanupLeaseLossCancelsEffectsAndRetainsFlag(t *testing.T) {
	failure := errors.New("lease replaced")
	var calls atomic.Int32
	repo := &recoveryRepo{op: Operation{CleanupRequired: true}, cleanupFence: func(context.Context, Operation) error {
		if calls.Add(1) > 1 {
			return failure
		}
		return nil
	}}
	service := Service{Repo: repo}
	err := service.cleanupWithInterval(context.Background(), repo.op, time.Second, time.Millisecond, func(ctx context.Context, _ Operation) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, failure) || !repo.op.CleanupRequired || len(repo.cleanupChanges) != 0 {
		t.Fatalf("lost cleanup lease cleared durable work: %v flag=%v changes=%v", err, repo.op.CleanupRequired, repo.cleanupChanges)
	}
}

func TestRecoveryCleanupRefusesMutationWithoutInitialLeaseFence(t *testing.T) {
	failure := errors.New("expired lease")
	repo := &recoveryRepo{op: Operation{CleanupRequired: true}, cleanupFence: func(context.Context, Operation) error { return failure }}
	service := Service{Repo: repo}
	called := false
	err := service.cleanupWithInterval(context.Background(), repo.op, time.Second, time.Millisecond, func(context.Context, Operation) error {
		called = true
		return nil
	})
	if !errors.Is(err, failure) || called || !repo.op.CleanupRequired {
		t.Fatalf("cleanup mutated without its lease: %v called=%v", err, called)
	}
}

func TestRecoveryCleanupFinalRenewalFailureRetainsFlag(t *testing.T) {
	failure := errors.New("final lease replaced")
	var calls atomic.Int32
	repo := &recoveryRepo{op: Operation{CleanupRequired: true}, cleanupFence: func(context.Context, Operation) error {
		if calls.Add(1) == 2 {
			return failure
		}
		return nil
	}}
	service := Service{Repo: repo}
	err := service.cleanupWithInterval(context.Background(), repo.op, time.Second, time.Hour, func(context.Context, Operation) error { return nil })
	if !errors.Is(err, failure) || calls.Load() != 2 || !repo.op.CleanupRequired || len(repo.cleanupChanges) != 0 {
		t.Fatalf("failed final renewal cleared work: %v calls=%d changes=%v", err, calls.Load(), repo.cleanupChanges)
	}
}

func TestRecoveryCleanupJoinsInFlightFenceBeforeFlagClear(t *testing.T) {
	failure := errors.New("in-flight lease replaced")
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	repo := &recoveryRepo{op: Operation{CleanupRequired: true}, cleanupFence: func(context.Context, Operation) error {
		if calls.Add(1) == 2 {
			close(started)
			<-release
			return failure
		}
		return nil
	}}
	service := Service{Repo: repo}
	err := service.cleanupWithInterval(context.Background(), repo.op, time.Second, time.Millisecond, func(ctx context.Context, _ Operation) error {
		select {
		case <-started:
			close(release)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if !errors.Is(err, failure) || !repo.op.CleanupRequired || len(repo.cleanupChanges) != 0 {
		t.Fatalf("in-flight fence was not joined before flag clear: %v changes=%v", err, repo.cleanupChanges)
	}
}
