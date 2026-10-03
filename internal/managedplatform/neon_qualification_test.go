package managedplatform

import (
	"strings"
	"testing"
)

func TestNeonReleaseSourceAndImageIdentity(t *testing.T) {
	if NeonReleaseQualificationID == "" || NeonReleaseSourceArchiveSHA256 == "" || NeonReleasePostgresCommit == "" || NeonReleaseConsumerPatchID == "" {
		t.Fatal("Neon release source identity is incomplete")
	}
	images := NeonReleaseImages()
	if len(images) != len(neonComponents) || !NeonReleaseImagesMatch(images) {
		t.Fatal("Neon candidate image inventory is incomplete")
	}
	for _, name := range []string{"changed", "missing", "extra"} {
		t.Run(name, func(t *testing.T) {
			candidate := NeonReleaseImages()
			switch name {
			case "changed":
				candidate["compute"] = "registry.example.test/compute@sha256:" + strings.Repeat("b", 64)
			case "missing":
				delete(candidate, "compute")
			case "extra":
				candidate["unreviewed"] = candidate["compute"]
			}
			if NeonReleaseImagesMatch(candidate) {
				t.Fatal("changed Neon image inventory retained its release binding")
			}
			if !NeonReleaseImagesMatch(NeonReleaseImages()) {
				t.Fatal("caller mutation changed the compiled Neon image inventory")
			}
		})
	}
}
