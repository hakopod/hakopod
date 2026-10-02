//go:build hakopod_native_acceptance

package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupabaseRotationAcceptanceBoundaryFailsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rotation-failed-once")
	release := filepath.Join(filepath.Dir(path), "rotation-release")
	t.Setenv("HAKOPOD_NATIVE_SUPABASE_ROTATION_FAIL_ONCE_FILE", path)
	t.Setenv("HAKOPOD_NATIVE_SUPABASE_ROTATION_RELEASE_FILE", release)
	if err := supabaseRotationAcceptanceBoundary(); err == nil || !strings.Contains(err.Error(), "injected a retry") {
		t.Fatalf("first boundary result = %v", err)
	}
	if err := supabaseRotationAcceptanceBoundary(); err == nil || !strings.Contains(err.Error(), "holding") {
		t.Fatalf("unreleased replay boundary result = %v", err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := supabaseRotationAcceptanceBoundary(); err != nil {
		t.Fatalf("released replay boundary result = %v", err)
	}
}
