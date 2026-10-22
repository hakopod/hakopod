package sandbox

import (
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
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

func TestCallTimeoutComesFromFrozenTemplate(t *testing.T) {
	r := Record{Service: "worker", Source: spec.Application{Services: map[string]spec.Service{"worker": {Session: &spec.SandboxSession{CallTimeoutSeconds: 600}}}}}
	if r.CallTimeout() != 600*time.Second {
		t.Fatal("template call timeout ignored")
	}
	r.Source.Services["worker"].Session.CallTimeoutSeconds = 0
	if r.CallTimeout() != DefaultCallTimeout || (Record{}).CallTimeout() != DefaultCallTimeout {
		t.Fatal("older receipts must keep the default call timeout")
	}
}
