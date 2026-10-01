package backup

import (
	"strings"
	"testing"
	"time"
)

func TestManagedMongoDBRecoveryRequiresVerifiedMatchingVersion(t *testing.T) {
	now := time.Now().UTC()
	valid := Artifact{Source: Source{Kind: "managed_database", ManagedDatabaseID: strings.Repeat("a", 32), Engine: "mongodb"}, Format: "age-v1+mongodb-bson-v1", SourceVersion: "8.0", CapturedAt: &now, VerifiedAt: &now}
	if err := valid.Source.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagedRecovery(valid, "mongodb", "8.0"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Artifact){
		func(a *Artifact) { a.SourceVersion = "7.0" },
		func(a *Artifact) { a.Format = "age-v1+mysql-sql" },
		func(a *Artifact) { a.Source.Engine = "mysql" },
		func(a *Artifact) { a.VerifiedAt = nil },
		func(a *Artifact) { a.CapturedAt = nil },
		func(a *Artifact) { a.DeletionPending = true },
	} {
		a := valid
		change(&a)
		if ValidateManagedRecovery(a, "mongodb", "8.0") == nil {
			t.Fatal("ineligible MongoDB archive accepted")
		}
	}
	if ValidateManagedRecovery(valid, "mongodb", "9.0") == nil {
		t.Fatal("unverified MongoDB upgrade accepted")
	}
	external := valid.Source
	external.Kind = "docker_import"
	external.ManagedDatabaseID = ""
	external.ExternalName = "source"
	if external.Validate() == nil {
		t.Fatal("unimplemented external MongoDB archive import accepted")
	}
}
