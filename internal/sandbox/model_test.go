package sandbox

import (
	"strings"
	"testing"
)

func TestSessionImagesRequireRepositoryAndDigest(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	if !ValidImage("registry.example/worker" + digest) {
		t.Fatal("valid pinned image rejected")
	}
	for _, image := range []string{digest, "https://registry.example/worker" + digest, "registry.example/worker:latest", "registry.example/worker@other" + digest, "worker image" + digest, "/worker" + digest, "../worker" + digest, "worker\x00" + digest} {
		if ValidImage(image) {
			t.Fatal("invalid image accepted")
		}
	}
}
