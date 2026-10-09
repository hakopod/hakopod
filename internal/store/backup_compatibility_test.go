package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
)

func TestBackupRecoverySetUsesAuthorizedStoredCapturePoints(t *testing.T) {
	s, p, _ := databaseFixture(t)
	ctx := context.Background()
	destination, err := s.PutBackupDestination(ctx, p, backup.Destination{ID: NewID(), Name: "recovery-set-fixture", EncryptedCredentials: []byte("fixture"), EncryptionRecipient: "age-fixture"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	capture := func(name string, point time.Time) backup.Artifact {
		spec := backup.ImportSpec{DestinationID: destination.ID, SourceName: name, Engine: "postgresql", SourceVersion: "17", CapturedAt: point, Bytes: 200, SHA256: strings.Repeat("a", 64)}
		input, err := s.PrepareBackupImport(ctx, p, spec, "compatibility-"+name)
		if err != nil {
			t.Fatal(err)
		}
		input, err = s.ClaimBackupImport(ctx, p, input.ID)
		if err != nil {
			t.Fatal(err)
		}
		verified := time.Now().UTC()
		artifact := backup.Artifact{ID: input.ID, JobID: input.ID, DestinationID: destination.ID, Source: backup.Source{Kind: "docker_import", Engine: "postgresql", ExternalName: name}, SourceVersion: "17", Bytes: 300, SHA256: strings.Repeat("b", 64), ObjectKey: "import/" + input.ID + ".age", Format: "age-v1+postgresql-custom", CapturedAt: &point, VerifiedAt: &verified}
		if err = s.FinishBackupImport(ctx, p, input, artifact); err != nil {
			t.Fatal(err)
		}
		return artifact
	}
	now := time.Now().UTC()
	first := capture("one", now.Add(-time.Hour))
	close := capture("two", now.Add(-time.Hour+time.Minute))
	late := capture("three", now.Add(-30*time.Minute))
	target := backup.Target{Source: backup.Source{Engine: "postgresql"}, SourceVersion: "17"}
	joined, err := s.BackupRecoverySet(ctx, p, first, []string{close.ID})
	if err != nil || len(joined.CompatibilityEvidence.RelatedRecoveryPoints) != 2 {
		t.Fatal(joined, err)
	}
	if backup.RestoreCompatibility(joined, target, true, now).Blocked {
		t.Fatal("close recovery points blocked")
	}
	joined, err = s.BackupRecoverySet(ctx, p, first, []string{late.ID})
	if err != nil || !backup.RestoreCompatibility(joined, target, true, now).Blocked {
		t.Fatal("inconsistent capture points accepted", err)
	}
	for _, ids := range [][]string{{first.ID}, {close.ID, close.ID}, make([]string, 17)} {
		if _, err = s.BackupRecoverySet(ctx, p, first, ids); !errors.Is(err, backup.ErrInput) {
			t.Fatal("invalid selection accepted", err)
		}
	}
	foreign := p
	foreign.Admin = false
	foreign.Owner = false
	foreign.Project = "other"
	foreign.Environment = "production"
	if _, err = s.BackupRecoverySet(ctx, foreign, first, []string{close.ID}); err == nil {
		t.Fatal("foreign capture point exposed")
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE backup_artifacts SET deletion_pending=true WHERE id=$1`, close.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BackupRecoverySet(ctx, p, first, []string{close.ID}); err == nil {
		t.Fatal("deleted capture point accepted")
	}
}
