package platformbackup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type Operation struct {
	ID                     string `json:"id"`
	Kind                   string `json:"kind"`
	Project                string `json:"project"`
	Environment            string `json:"environment"`
	Status                 string `json:"status"`
	Phase                  string `json:"phase"`
	Message                string `json:"message"`
	SourcePlatformID       string `json:"source_platform_id"`
	TargetPlatformID       string `json:"target_platform_id,omitempty"`
	ArtifactID             string `json:"artifact_id,omitempty"`
	ResultArtifactID       string `json:"result_artifact_id,omitempty"`
	DestinationID          string `json:"destination_id,omitempty"`
	DestinationRevision    int64  `json:"destination_revision,omitempty"`
	ExpectedSourceRevision int64  `json:"expected_source_revision"`
	ExpectedTargetRevision int64  `json:"expected_target_revision,omitempty"`
	AuthorityFingerprint   []byte `json:"-"`
	Lease                  string `json:"-"`
	CancelRequested        bool   `json:"cancel_requested"`
	CleanupRequired        bool   `json:"-"`
}

type Repository interface {
	ClaimPlatformRecovery(context.Context, string) (Operation, error)
	ReauthorizePlatformRecovery(context.Context, Operation) error
	HeartbeatPlatformRecovery(context.Context, Operation) (bool, error)
	StepPlatformRecovery(context.Context, Operation, string, string) error
	FinishPlatformRecovery(context.Context, Operation, string, string) error
	SetPlatformRecoveryCleanup(context.Context, Operation, bool) error
}

type CapturedPart struct {
	Part
	Open    func() (io.ReadCloser, error)
	Cleanup func() error
}

// Runtime owns Kubernetes admission and all in-cluster data movement. Pause
// must close public ingress and every Auth, Storage, Realtime and PostgREST
// write path before Drain returns. Restore keeps the target closed.
type Runtime interface {
	ResolveSource(context.Context, Operation) (Manifest, error)
	ResolveEmptyTarget(context.Context, Operation, Manifest) error
	PauseWrites(context.Context, Operation) error
	DrainWrites(context.Context, Operation) error
	Capture(context.Context, Operation, Manifest) ([]CapturedPart, map[string]string, error)
	ReobserveSourceClaims(context.Context, Operation, Manifest) error
	ResumeSource(context.Context, Operation) error
	RestorePart(context.Context, Operation, Manifest, Part, io.Reader) error
	VerifyRestoredContent(context.Context, Operation, Manifest) error
	VerifyRestoredRuntime(context.Context, Operation, Manifest) error
}

type ArtifactStore interface {
	PutEncrypted(context.Context, Operation, Manifest, func(io.Writer) error) (string, error)
	OpenDecrypted(context.Context, string) (io.ReadCloser, error)
	Publish(context.Context, Operation, Manifest, string) error
	Discard(context.Context, string) error
}

type Service struct {
	Repo      Repository
	Runtime   Runtime
	Artifacts ArtifactStore
	Timeout   time.Duration
}

var errRecoveryCleanupRequired = errors.New("recovery cleanup remains required")

type recoveryOperationContextKey struct{}
type recoveryCleanupContextKey struct{}

func WithRecoveryOperation(ctx context.Context, op Operation) context.Context {
	return context.WithValue(ctx, recoveryOperationContextKey{}, op)
}
func RecoveryOperationFromContext(ctx context.Context) (Operation, bool) {
	op, ok := ctx.Value(recoveryOperationContextKey{}).(Operation)
	return op, ok
}
func WithRecoveryCleanup(ctx context.Context) context.Context {
	return context.WithValue(ctx, recoveryCleanupContextKey{}, true)
}
func RecoveryCleanupFromContext(ctx context.Context) bool {
	cleanup, _ := ctx.Value(recoveryCleanupContextKey{}).(bool)
	return cleanup
}

func (s *Service) RunOnce(parent context.Context) error {
	op, err := s.Repo.ClaimPlatformRecovery(parent, randomLease())
	if err != nil {
		return err
	}
	timeout := s.Timeout
	if timeout <= 0 || timeout > 2*time.Hour {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	ctx = WithRecoveryOperation(ctx, op)
	defer cancel()
	if op.CleanupRequired {
		err = s.resume(ctx, op)
		if err != nil {
			return errors.Join(errRecoveryCleanupRequired, err)
		}
		return s.finish(parent, op, "cancelled", "Interrupted backup cleanup completed before terminalization.")
	}
	if err = s.Repo.ReauthorizePlatformRecovery(ctx, op); err != nil {
		return s.finish(parent, op, "cancelled", "Operation authority is no longer valid.")
	}
	if op.CancelRequested {
		return s.finish(parent, op, "cancelled", "Operation was cancelled before another recovery step began.")
	}
	var heartbeat sync.WaitGroup
	heartbeat.Add(1)
	go func() {
		defer heartbeat.Done()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cancelled, e := s.Repo.HeartbeatPlatformRecovery(ctx, op)
				if e != nil || cancelled {
					cancel()
					return
				}
			}
		}
	}()
	switch op.Kind {
	case "backup":
		err = s.backup(ctx, op)
	case "restore":
		err = s.restore(ctx, op)
	default:
		err = fmt.Errorf("unknown platform recovery operation")
	}
	wasCancelled := ctx.Err() != nil || errors.Is(err, context.Canceled)
	cancel()
	heartbeat.Wait()
	if err != nil {
		if errors.Is(err, errRecoveryCleanupRequired) {
			return err
		}
		status, message := "failed", "Supabase backup or restore failed; inspect the bounded operation phase and keep any partial target isolated."
		if wasCancelled {
			status, message = "cancelled", "Operation was cancelled, timed out, or lost its authority; the target remains isolated for inspection."
		}
		if finishErr := s.finish(parent, op, status, message); finishErr != nil {
			return errors.Join(err, finishErr)
		}
		return err
	}
	return s.finish(parent, op, "succeeded", "")
}

func (s *Service) finish(parent context.Context, op Operation, status, message string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	return s.Repo.FinishPlatformRecovery(ctx, op, status, message)
}

func (s *Service) checkpoint(ctx context.Context, op Operation, phase string) error {
	if err := s.Repo.ReauthorizePlatformRecovery(ctx, op); err != nil {
		return err
	}
	cancelled, err := s.Repo.HeartbeatPlatformRecovery(ctx, op)
	if err != nil {
		return err
	}
	if cancelled {
		return context.Canceled
	}
	return s.Repo.StepPlatformRecovery(ctx, op, phase, "")
}

func (s *Service) backup(ctx context.Context, op Operation) (result error) {
	manifest, err := s.Runtime.ResolveSource(ctx, op)
	if err != nil {
		return err
	}
	if err = s.checkpoint(ctx, op, "pausing-writes"); err != nil {
		return err
	}
	if err = s.Repo.SetPlatformRecoveryCleanup(ctx, op, true); err != nil {
		return err
	}
	paused := true
	defer func() {
		if paused {
			if e := s.resume(ctx, op); e != nil {
				if result != nil {
					result = errors.Join(result, errRecoveryCleanupRequired, e)
				} else {
					result = errors.Join(errRecoveryCleanupRequired, e)
				}
			}
		}
	}()
	if err = s.Runtime.PauseWrites(ctx, op); err != nil {
		return err
	}
	if err = s.checkpoint(ctx, op, "draining-writes"); err != nil {
		return err
	}
	if err = s.Runtime.DrainWrites(ctx, op); err != nil {
		return err
	}
	manifest.FrozenAt = time.Now().UTC()
	if err = s.checkpoint(ctx, op, "capturing"); err != nil {
		return err
	}
	parts, evidence, err := s.Runtime.Capture(ctx, op, manifest)
	if err != nil {
		return err
	}
	manifest.Parts = nil
	manifest.Verification = evidence
	defer func() {
		for _, part := range parts {
			if part.Cleanup != nil {
				_ = part.Cleanup()
			}
		}
	}()
	open := map[string]func() (io.ReadCloser, error){}
	for _, part := range parts {
		manifest.Parts = append(manifest.Parts, part.Part)
		open[part.Name] = part.Open
	}
	manifest.CapturedAt = time.Now().UTC()
	if err = s.Runtime.ReobserveSourceClaims(ctx, op, manifest); err != nil {
		return fmt.Errorf("source ownership changed during capture: %w", err)
	}
	if err = s.resume(ctx, op); err != nil {
		return err
	}
	paused = false
	manifest.ThawedAt = time.Now().UTC()
	manifest.ManifestSHA256 = manifest.Digest()
	if err = manifest.Validate(); err != nil {
		return err
	}
	var temporary string
	temporary, err = s.Artifacts.PutEncrypted(ctx, op, manifest, func(w io.Writer) error {
		return WriteArchive(w, manifest, func(part Part) (io.ReadCloser, error) {
			fn := open[part.Name]
			if fn == nil {
				return nil, ErrInvalid
			}
			return fn()
		})
	})
	if err != nil {
		return err
	}
	defer func() {
		if temporary != "" {
			_ = s.Artifacts.Discard(context.WithoutCancel(ctx), temporary)
		}
	}()
	if err = s.checkpoint(ctx, op, "publishing"); err != nil {
		return err
	}
	if err = s.Artifacts.Publish(ctx, op, manifest, temporary); err != nil {
		return err
	}
	temporary = ""
	return nil
}

func (s *Service) resume(parent context.Context, op Operation) error {
	ctx, cancel := context.WithTimeout(WithRecoveryCleanup(context.WithoutCancel(parent)), 30*time.Second)
	defer cancel()
	if err := s.Runtime.ResumeSource(ctx, op); err != nil {
		return err
	}
	return s.Repo.SetPlatformRecoveryCleanup(ctx, op, false)
}

func (s *Service) restore(ctx context.Context, op Operation) (result error) {
	admittedBefore := op.Phase == "target-admitted" || strings.HasPrefix(op.Phase, "restoring-") || strings.HasPrefix(op.Phase, "verifying-")
	admittedMutation := admittedBefore
	defer func() {
		if admittedMutation && result != nil && !errors.Is(result, errRecoveryCleanupRequired) {
			result = errors.Join(result, errRecoveryCleanupRequired)
		}
	}()
	r, err := s.Artifacts.OpenDecrypted(ctx, op.ArtifactID)
	if err != nil {
		return err
	}
	verified, err := os.CreateTemp("", "hakopod-verified-recovery-*")
	if err != nil {
		_ = r.Close()
		return err
	}
	verifiedName := verified.Name()
	defer func() { _ = verified.Close(); _ = os.Remove(verifiedName) }()
	written, copyErr := io.Copy(verified, io.LimitReader(r, MaxArchiveBytes+MaxManifestBytes+(1<<20)+1))
	closeErr := r.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > MaxArchiveBytes+MaxManifestBytes+(1<<20) {
		return ErrInvalid
	}
	if _, err = verified.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err = ReadArchiveWithManifest(verified, func(Manifest) error { return nil }, func(_ Part, input io.Reader) error { _, e := io.Copy(io.Discard, input); return e }); err != nil {
		return err
	}
	if _, err = verified.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var admitted Manifest
	manifest, err := ReadArchiveWithManifest(verified, func(source Manifest) error {
		if !admittedBefore {
			if err := s.checkpoint(ctx, op, "admitting-empty-target"); err != nil {
				return err
			}
			admittedMutation = true
			if err := s.Runtime.ResolveEmptyTarget(ctx, op, source); err != nil {
				return err
			}
			if err := s.checkpoint(ctx, op, "target-admitted"); err != nil {
				return err
			}
		}
		admitted = source
		return nil
	}, func(part Part, input io.Reader) error {
		if admitted.PlatformID == "" {
			return fmt.Errorf("restore target was not admitted before data")
		}
		if err := s.checkpoint(ctx, op, "restoring-"+part.Name); err != nil {
			return err
		}
		return s.Runtime.RestorePart(ctx, op, admitted, part, input)
	})
	if err != nil {
		return err
	}
	if manifest.ManifestSHA256 != admitted.ManifestSHA256 {
		return fmt.Errorf("manifest changed during restore")
	}
	if err = s.checkpoint(ctx, op, "verifying-content"); err != nil {
		return err
	}
	if err = s.Runtime.VerifyRestoredContent(ctx, op, manifest); err != nil {
		return err
	}
	if err = s.checkpoint(ctx, op, "verifying-runtime"); err != nil {
		return err
	}
	return s.Runtime.VerifyRestoredRuntime(ctx, op, manifest)
}

func randomLease() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data)
}
