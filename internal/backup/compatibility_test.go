package backup

import (
	"testing"
	"time"
)

func TestRestoreCompatibilityReportsEvidenceAndUnknowns(t *testing.T) {
	now := time.Now().UTC()
	a := Artifact{Source: Source{Kind: "database", Engine: "postgresql"}, SourceVersion: "17", CapturedAt: ptrTime(now.Add(-time.Hour))}
	report := RestoreCompatibility(a, Target{Source: Source{Engine: "postgresql"}}, true, now)
	if report.Blocked || len(report.Checks) != 6 || report.Checks[0].Status != "checked" || report.Checks[3].Status != "unknown" {
		t.Fatalf("unexpected compatibility report: %#v", report)
	}
	if !RestoreCompatibility(a, Target{Source: Source{Engine: "mysql"}}, true, now).Blocked || !RestoreCompatibility(a, Target{Source: Source{Engine: "postgresql"}}, false, now).Blocked {
		t.Fatal("engine and key blockers were not preserved")
	}
	a.CompatibilityEvidence.RelatedRecoveryPoints = map[string]time.Time{"db": now.Add(-time.Hour), "files": now.Add(-30 * time.Minute)}
	if !RestoreCompatibility(a, Target{Source: Source{Engine: "postgresql"}}, true, now).Blocked {
		t.Fatal("inconsistent related recovery points were accepted")
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
