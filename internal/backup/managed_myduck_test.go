package backup

import (
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

func TestMyDuckManagedRecoveryRequiresExactColdArchive(t *testing.T) {
	now := time.Now()
	valid := Artifact{Source: Source{Kind: "managed_database", ManagedDatabaseID: strings.Repeat("a", 32), Engine: "duckdb"}, Format: "age-v1+myduck-cold-v1", SourceVersion: database.MyDuckVersion, CapturedAt: &now, VerifiedAt: &now}
	if err := ValidateManagedRecovery(valid, "duckdb", valid.SourceVersion); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Artifact){func(a *Artifact) { a.Format = "age-v1+postgresql-custom" }, func(a *Artifact) { a.SourceVersion = "0.3.1" }, func(a *Artifact) { a.VerifiedAt = nil }, func(a *Artifact) { a.Source.Engine = "postgresql" }} {
		next := valid
		change(&next)
		if ValidateManagedRecovery(next, "duckdb", database.MyDuckVersion) == nil {
			t.Fatal("unsafe MyDuck cold recovery was accepted")
		}
	}
}
