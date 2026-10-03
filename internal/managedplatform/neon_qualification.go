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

// The immutable candidate image inventory is release-visible only after the
// exact source and images pass native acceptance and the gate opens.
var neonReleaseImages = map[string]string{
	"broker":              "ghcr.io/hakopod/neon-storage@sha256:a787c50ec7a89677e6ee2b17878bb251ecb2f2cc8c7a1f267a3c78e900b69e24",
	"compute":             "ghcr.io/hakopod/neon-compute-v17@sha256:51ecb134e515abd0b30956f07b5ce8cd9740a791d9ff9d4b658143e9d2be2610",
	"compute-tls":         "ghcr.io/hakopod/neon-compute-tls@sha256:edd0d8aa4edcb1a79ee3341eb5047c7e9ef75bdc441ccea07c00cd3325cf4fda",
	"controller-database": "ghcr.io/hakopod/neon-controller-database@sha256:26e0e6816f8c67af5ef256255562da07b03fe3368f2aa7585ce08b85bdd4c586",
	"pageserver":          "ghcr.io/hakopod/neon-storage@sha256:a787c50ec7a89677e6ee2b17878bb251ecb2f2cc8c7a1f267a3c78e900b69e24",
	"proxy":               "ghcr.io/hakopod/neon-storage@sha256:a787c50ec7a89677e6ee2b17878bb251ecb2f2cc8c7a1f267a3c78e900b69e24",
	"safekeeper":          "ghcr.io/hakopod/neon-storage@sha256:a787c50ec7a89677e6ee2b17878bb251ecb2f2cc8c7a1f267a3c78e900b69e24",
	"storage-controller":  "ghcr.io/hakopod/neon-storage@sha256:a787c50ec7a89677e6ee2b17878bb251ecb2f2cc8c7a1f267a3c78e900b69e24",
}

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
