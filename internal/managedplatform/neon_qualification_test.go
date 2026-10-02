package managedplatform

import "testing"

func TestNeonReleaseGateAndSourceIdentityRemainClosed(t *testing.T) {
	if NeonReleaseQualified() {
		t.Fatal("unqualified Neon release gate is open")
	}
	if NeonReleaseQualificationID == "" || NeonReleaseSourceArchiveSHA256 == "" || NeonReleasePostgresCommit == "" || NeonReleaseConsumerPatchID == "" {
		t.Fatal("Neon release source identity is incomplete")
	}
	if NeonReleaseImagesMatch(map[string]string{}) || len(NeonReleaseImages()) != 0 {
		t.Fatal("Neon release exposed an image inventory before native qualification")
	}
}
