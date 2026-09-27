package backup

import (
	"testing"
	"time"
)

func TestManagedRecoveryRequiresVerifiedSupportedArchive(t *testing.T) {
	now := time.Now().UTC()
	valid := Artifact{Source: Source{Kind: "managed_database", Engine: "postgresql"}, Format: "age-v1+postgresql-custom", SourceVersion: "17", CapturedAt: &now, VerifiedAt: &now}
	for _, version := range []string{"17", "18"} {
		if err := ValidateManagedRecovery(valid, "postgresql", version); err != nil {
			t.Fatal(version, err)
		}
	}
	for _, change := range []func(*Artifact){func(a *Artifact) { a.SourceVersion = "" }, func(a *Artifact) { a.SourceVersion = "16" }, func(a *Artifact) { a.VerifiedAt = nil }, func(a *Artifact) { a.CapturedAt = nil }, func(a *Artifact) { a.Format = "sql" }, func(a *Artifact) { a.DeletionPending = true }, func(a *Artifact) { a.Source.Engine = "redis" }} {
		a := valid
		change(&a)
		if ValidateManagedRecovery(a, "postgresql", "18") == nil {
			t.Fatal("ineligible archive accepted")
		}
	}
	valid.SourceVersion = "18"
	if ValidateManagedRecovery(valid, "postgresql", "17") == nil {
		t.Fatal("downgrade accepted")
	}
}
