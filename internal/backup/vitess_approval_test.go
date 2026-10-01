package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVitessApprovalRequiresDedicatedExactScope(t *testing.T) {
	valid := VitessBackupApproval{DestinationID: strings.Repeat("a", 32), Revision: 3, Project: "project", Environment: "development", Database: "analytics", DedicatedCredentials: true, EndpointCIDRs: []string{"93.184.216.34/32"}}
	if _, err := FindVitessBackupApproval([]VitessBackupApproval{valid}, valid.DestinationID, 3, "project", "development", "analytics"); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*VitessBackupApproval){
		"unscoped keys":         func(a *VitessBackupApproval) { a.DedicatedCredentials = false },
		"different revision":    func(a *VitessBackupApproval) { a.Revision++ },
		"different project":     func(a *VitessBackupApproval) { a.Project = "other" },
		"different environment": func(a *VitessBackupApproval) { a.Environment = "production" },
		"different database":    func(a *VitessBackupApproval) { a.Database = "other" },
		"broad network":         func(a *VitessBackupApproval) { a.EndpointCIDRs = []string{"93.184.216.0/24"} },
		"private network":       func(a *VitessBackupApproval) { a.EndpointCIDRs = []string{"10.0.0.1/32"} },
		"metadata endpoint":     func(a *VitessBackupApproval) { a.EndpointCIDRs = []string{"169.254.169.254/32"} },
		"mapped IPv4":           func(a *VitessBackupApproval) { a.EndpointCIDRs = []string{"::ffff:93.184.216.34/128"} },
	} {
		t.Run(name, func(t *testing.T) {
			a := valid
			change(&a)
			if _, err := FindVitessBackupApproval([]VitessBackupApproval{a}, valid.DestinationID, 3, "project", "development", "analytics"); err == nil {
				t.Fatal("unsafe or mismatched approval accepted")
			}
		})
	}
	if ValidateVitessBackupApprovals([]VitessBackupApproval{valid, valid}) == nil {
		t.Fatal("duplicate destination approval accepted")
	}
}

func TestVitessApprovalFileIsBoundedAndStrict(t *testing.T) {
	file := filepath.Join(t.TempDir(), "vitess.toml")
	valid := "schema_version = 1\n[[approvals]]\ndestination_id = '" + strings.Repeat("a", 32) + "'\nrevision = 1\nproject = 'project'\nenvironment = 'development'\ndatabase = 'analytics'\ndedicated_credentials = true\nendpoint_cidrs = ['93.184.216.34/32']\n"
	for _, content := range []string{valid, valid + "secret_access_key = 'not-accepted-here'\n", strings.Repeat("x", 65537)} {
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := ReadVitessBackupApprovals(file)
		if (err == nil) != (content == valid) {
			t.Fatalf("strict file acceptance = %v", err)
		}
	}
	if err := os.WriteFile(file, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadVitessBackupApprovals(file); err == nil {
		t.Fatal("writable operator authority accepted")
	}
}

func TestManagedVitessRecoveryBoundary(t *testing.T) {
	now := time.Now().UTC()
	valid := Artifact{Source: Source{Kind: "managed_database", ManagedDatabaseID: strings.Repeat("a", 32), Engine: "vitess"}, Format: "age-v1+vitess-logical-v1", SourceVersion: "23", CapturedAt: &now, VerifiedAt: &now}
	if err := valid.Source.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagedRecovery(valid, "vitess", "23"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Artifact){func(a *Artifact) { a.Format = "age-v1+mysql-sql" }, func(a *Artifact) { a.SourceVersion = "22" }, func(a *Artifact) { a.VerifiedAt = nil }, func(a *Artifact) { a.Source.Engine = "mysql" }, func(a *Artifact) { a.DeletionPending = true }} {
		a := valid
		change(&a)
		if ValidateManagedRecovery(a, "vitess", "23") == nil {
			t.Fatal("ineligible shard archive accepted")
		}
	}
}
