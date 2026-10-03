package managedplatform

import "testing"

func TestNeonReleaseGateAndSourceIdentityRemainClosed(t *testing.T) {
	if NeonReleaseQualified() {
		t.Fatal("unqualified Neon release gate is open")
	}
	if NeonReleaseQualificationID == "" || NeonReleaseSourceArchiveSHA256 == "" || NeonReleasePostgresCommit == "" || NeonReleaseConsumerPatchID == "" {
		t.Fatal("Neon release source identity is incomplete")
	}
	images := NeonReleaseImages()
	if len(images) != len(neonComponents) || !NeonReleaseImagesMatch(images) {
		t.Fatal("Neon candidate image inventory is incomplete")
	}
}
