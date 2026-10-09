package platformbackup

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRestoreCompatibilityUsesOnlyManifestEvidence(t *testing.T) {
	now := time.Now().UTC()
	m := Manifest{CapturedAt: now.Add(-time.Hour), PlatformSpec: json.RawMessage(`{"kind":"supabase"}`), Release: "supabase-1", Images: map[string]string{"api": "api@sha256:" + string(make([]byte, 64))}, EncryptionRecipient: "age1fixture", PVCs: []Claim{{Name: "data"}}}
	report := RestoreCompatibility(m, "supabase", now)
	if report.Blocked || len(report.Checks) != 5 || report.Checks[4].Status != "unknown" {
		t.Fatalf("unexpected report: %#v", report)
	}
	m.EncryptionRecipient = ""
	if !RestoreCompatibility(m, "supabase", now).Blocked {
		t.Fatal("missing key reference was accepted")
	}
}
