package backup

import (
	"strings"
	"testing"
	"time"
)

func TestManagedOracleRecoveryFormatAndVersionBoundary(t *testing.T) {
	now := time.Now().UTC()
	valid := Artifact{Source: Source{Kind: "managed_database", ManagedDatabaseID: strings.Repeat("a", 32), Engine: "oracle"}, Format: "age-v1+oracle-datapump-v1", SourceVersion: "23.26", CapturedAt: &now, VerifiedAt: &now}
	if err := valid.Source.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagedRecovery(valid, "oracle", "23.26"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Artifact){func(a *Artifact) { a.SourceVersion = "19" }, func(a *Artifact) { a.Format = "age-v1+oracle-rman" }, func(a *Artifact) { a.Source.Engine = "mysql" }, func(a *Artifact) { a.VerifiedAt = nil }, func(a *Artifact) { a.DeletionPending = true }} {
		next := valid
		change(&next)
		if ValidateManagedRecovery(next, "oracle", "23.26") == nil {
			t.Fatal("ineligible Oracle recovery was accepted")
		}
	}
	if ValidateManagedRecovery(valid, "oracle", "19") == nil {
		t.Fatal("Oracle recovery silently crossed versions")
	}
}
