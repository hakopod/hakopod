package managedplatform

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const NeonReleaseQualificationID = "neon-fa504217-pg17.11-linux-amd64"
const NeonReleaseSourceArchiveSHA256 = "f91a9e1a5945dd1d2e634c2438d5c11052d4f2e993dc2d09c82f25d6eff995da"
const NeonReleasePostgresCommit = "1e01fcea2a6b38180021aa83e0051d95286d9096"
const NeonReleaseConsumerPatchID = "627583b85d7c0f624245e3ac6a240e775a5eed01"

// The immutable release image inventory is populated only after the candidate
// images have passed the committed native acceptance run.
var neonReleaseImages = map[string]string{}

// NeonReleaseQualified changes only after the exact source, image inventory,
// runtime, recovery and real-cluster evidence pass the release verifier.
func NeonReleaseQualified() bool { return false }

func NeonReleaseImages() map[string]string {
	images := make(map[string]string, len(neonReleaseImages))
	for name, image := range neonReleaseImages {
		images[name] = image
	}
	return images
}

func NeonReleaseImagesMatch(images map[string]string) bool {
	return len(neonReleaseImages) == len(neonComponents) && len(images) == len(neonReleaseImages) &&
		NeonImageInventorySHA256(images) == NeonImageInventorySHA256(neonReleaseImages)
}

func NeonImageInventorySHA256(images map[string]string) string {
	items := make([]string, 0, len(neonComponents))
	for _, name := range neonComponents {
		items = append(items, name+"="+images[name])
	}
	digest := sha256.Sum256([]byte(strings.Join(items, "\n")))
	return hex.EncodeToString(digest[:])
}
