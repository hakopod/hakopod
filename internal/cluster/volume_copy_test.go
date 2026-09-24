package cluster

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestOfflineVolumeCopyFilesystemIntegrity(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux filesystem/xattr acceptance runs in CI and the migration image")
	}
	out, err := exec.Command("python3", "test_volume_copy.py").CombinedOutput()
	if err != nil {
		t.Fatalf("filesystem migration: %v\n%s", err, out)
	}
}
