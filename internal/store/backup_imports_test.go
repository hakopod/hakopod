package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
)

func TestBackupImportReviewLeaseAndCommit(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	d, err := s.PutBackupDestination(ctx, p, backup.Destination{ID: NewID(), Name: "import-development-fixture", EncryptedCredentials: []byte("sealed-test-fixture")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	spec := backup.ImportSpec{DestinationID: d.ID, SourceName: "docker-orders", Engine: "postgresql", SourceVersion: "17", CapturedAt: time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond), Bytes: 200, SHA256: strings.Repeat("a", 64)}
	i, err := s.PrepareBackupImport(ctx, p, spec, "import-review-fixture")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.PrepareBackupImport(ctx, p, spec, "import-review-fixture")
	if err != nil || replay.ID != i.ID {
		t.Fatal("review replay", err)
	}
	if _, err = s.PutBackupDestination(ctx, p, d, d.Revision); !errors.Is(err, backup.ErrConflict) {
		t.Fatal("destination changed during import", err)
	}
	claimed, err := s.ClaimBackupImport(ctx, p, i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimBackupImport(ctx, p, i.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("concurrent upload accepted", err)
	}
	if err = s.CheckBackupImport(ctx, p, claimed); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := backup.Artifact{ID: i.ID, JobID: i.ID, DestinationID: d.ID, Source: backup.Source{Kind: "docker_import", Engine: "postgresql", ExternalName: spec.SourceName}, SourceVersion: "17", Bytes: 300, SHA256: strings.Repeat("b", 64), ObjectKey: "import/" + i.ID + ".age", Format: "age-v1+postgresql-custom", CapturedAt: &spec.CapturedAt, VerifiedAt: &now}
	if err = s.FinishBackupImport(ctx, p, claimed, a); err != nil {
		t.Fatal("atomic import commit", err)
	}
	completed, err := s.ClaimBackupImport(ctx, p, i.ID)
	if err != nil || completed.Status != "completed" || completed.ArtifactID != a.ID {
		t.Fatal("completed replay", err)
	}
	if _, err = s.BackupArtifact(ctx, a.ID); err != nil {
		t.Fatal("artifact absent", err)
	}
	job, err := s.BackupJob(ctx, i.ID)
	if err != nil || job.Status != "succeeded" {
		t.Fatal("import job absent", err)
	}
	if err = s.FinishBackupImport(ctx, p, claimed, a); !errors.Is(err, ErrConflict) {
		t.Fatal("stale upload committed twice", err)
	}
}
func TestBackupImportExpiredUploadCleanupAndRevocation(t *testing.T) {
	s := isolatedDatabase(t)
	p := bootstrapPrincipal(t, s)
	ctx := context.Background()
	d, err := s.PutBackupDestination(ctx, p, backup.Destination{ID: NewID(), Name: "import-cleanup-fixture", EncryptedCredentials: []byte("sealed-test-fixture")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	spec := backup.ImportSpec{DestinationID: d.ID, SourceName: "docker-fixture", Engine: "redis", SourceVersion: "8", CapturedAt: time.Now().Add(-time.Hour), Bytes: 200, SHA256: strings.Repeat("a", 64)}
	i, err := s.PrepareBackupImport(ctx, p, spec, "cleanup-review-fixture")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimBackupImport(ctx, p, i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE backup_imports SET lease_until=now()-interval '1 second' WHERE id=$1", i.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimBackupImport(ctx, p, i.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("expired lease bypassed cleanup", err)
	}
	calls := 0
	if err = s.CleanupBackupImports(ctx, func(ctx context.Context, old backup.Import) error {
		calls++
		if old.ID != i.ID {
			t.Fatal("wrong cleanup import")
		}
		if _, e := s.ClaimBackupImport(ctx, p, i.ID); !errors.Is(e, ErrConflict) {
			t.Fatal("retry raced cleanup", e)
		}
		return nil
	}); err != nil || calls != 1 {
		t.Fatal("cleanup", err, calls)
	}
	retry, err := s.ClaimBackupImport(ctx, p, i.ID)
	if err != nil || retry.Lease == claimed.Lease {
		t.Fatal("retry lease", err)
	}
	if err = s.CheckBackupImport(ctx, p, claimed); !errors.Is(err, ErrConflict) {
		t.Fatal("stale lease still valid", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", p.KeyID); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckBackupImport(ctx, p, retry); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked key continued upload", err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE backup_imports SET lease_until=now()-interval '1 second', expires_at=now()-interval '1 second' WHERE id=$1", i.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.CleanupBackupImports(ctx, func(context.Context, backup.Import) error { return nil }); err != nil {
		t.Fatal("revoked import cleanup", err)
	}
	var status string
	if err = s.Pool.QueryRow(ctx, "SELECT status FROM backup_imports WHERE id=$1", i.ID).Scan(&status); err != nil || status != "expired" {
		t.Fatal("expiry not recorded", status, err)
	}
}
