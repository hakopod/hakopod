package api

import (
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/backup"
)

// The restore review is what an operator reads before retyping a database name
// to authorize a destructive restore, so the wrong warning set is a lie told at
// the worst moment. Nothing else asserts which set is chosen.
func TestRestoreWarningsMatchTheBackupKind(t *testing.T) {
	dump := restoreWarnings(backup.Source{Kind: "database", Engine: "postgresql"})
	engine := restoreWarnings(backup.Source{Kind: "database", Engine: "clickhouse"})
	joinedDump, joinedEngine := strings.Join(dump, " "), strings.Join(engine, " ")
	if !strings.Contains(joinedDump, "age authentication") {
		t.Fatal("a logical dump must still promise the verification it performs:", joinedDump)
	}
	if strings.Contains(joinedEngine, "age authentication") || strings.Contains(joinedEngine, "checksum are verified") {
		t.Fatal("an engine-managed restore must not claim verification that never happens:", joinedEngine)
	}
	for _, want := range []string{"did not read, decrypt or checksum", "listed the destination prefix"} {
		if !strings.Contains(joinedEngine, want) {
			t.Fatalf("the engine-managed warnings must say %q: %s", want, joinedEngine)
		}
	}
	management := restoreWarnings(backup.Source{Kind: "management", Engine: "postgresql"})
	if len(management) <= len(dump) {
		t.Fatal("a management restore must keep its extra warning")
	}
}
