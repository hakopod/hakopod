package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func writeManagedPlatformConfig(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "managed-platforms.toml")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadManagedPlatformFileRejectsUnsafeFiles(t *testing.T) {
	t.Run("mode", func(t *testing.T) {
		path := writeManagedPlatformConfig(t, "schema_version = 1\n", 0640)
		if _, err := loadManagedPlatformFile(path); err == nil || !strings.Contains(err.Error(), "mode 0600") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		target := writeManagedPlatformConfig(t, "schema_version = 1\n", 0600)
		link := filepath.Join(t.TempDir(), "runtime.toml")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := loadManagedPlatformFile(link); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		path := writeManagedPlatformConfig(t, "schema_version = 1\nunknown = true\n", 0600)
		if _, err := loadManagedPlatformFile(path); err == nil || !strings.Contains(err.Error(), "decode") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("size", func(t *testing.T) {
		path := writeManagedPlatformConfig(t, "schema_version = 1\n", 0600)
		if err := os.Truncate(path, managedPlatformConfigMaxBytes+1); err != nil {
			t.Fatal(err)
		}
		if _, err := loadManagedPlatformFile(path); err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("trusted Neon proxy origin", func(t *testing.T) {
		path := writeManagedPlatformConfig(t, "schema_version = 1\nneon_proxy_control_plane_origin = \"https://control.example.test\"\n", 0600)
		config, err := loadManagedPlatformFile(path)
		if err != nil || config.NeonProxyControlPlaneOrigin != "https://control.example.test" {
			t.Fatalf("trusted origin was not decoded: %v", err)
		}
	})
}

func TestValidateManagedPlatformSecretsBoundsAndScopes(t *testing.T) {
	valid := map[string]map[string]map[string]map[string]string{"project-a": {"development": {"runtime-r1": {"token": "value"}}}}
	if err := validateManagedPlatformSecrets(valid); err != nil {
		t.Fatal(err)
	}
	if err := validateManagedPlatformSecrets(map[string]map[string]map[string]map[string]string{"": {"development": {"runtime-r1": {"token": "value"}}}}); err == nil {
		t.Fatal("empty project accepted")
	}
	large := strings.Repeat("x", managedPlatformMaxSecretValueBytes+1)
	if err := validateManagedPlatformSecrets(map[string]map[string]map[string]map[string]string{"project-a": {"development": {"runtime-r1": {"token": large}}}}); err == nil {
		t.Fatal("oversized secret value accepted")
	}
}

func TestConfigureManagedPlatformsRejectsNilDependencies(t *testing.T) {
	if _, _, _, err := configureManagedPlatforms("configured", nil, nil, ""); err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateManagedPlatformInventoriesKeepsPlatformsIndependent(t *testing.T) {
	images := func(names []string) map[string]string {
		values := make(map[string]string, len(names))
		for _, name := range names {
			values[name] = "registry.example.test/platform/" + name + "@sha256:" + strings.Repeat("a", 64)
		}
		return values
	}
	supabaseIdentities := map[string]managedplatform.RuntimeIdentity{}
	for _, name := range managedplatform.SupabaseComponentNames() {
		supabaseIdentities[name] = managedplatform.RuntimeIdentity{UID: 10001, GID: 10001}
	}
	if supabase, neon, err := validateManagedPlatformInventories(managedPlatformFile{Images: images(managedplatform.SupabaseComponentNames()), Identities: supabaseIdentities}); err != nil || !supabase || neon {
		t.Fatalf("Supabase-only inventory was not accepted: supabase=%t neon=%t err=%v", supabase, neon, err)
	}
	neonIdentities := map[string]managedplatform.NeonRuntimeIdentity{}
	for _, name := range managedplatform.NeonComponents() {
		neonIdentities[name] = managedplatform.NeonRuntimeIdentity{UID: 10001, GID: 10001}
	}
	config := managedPlatformFile{NeonImages: images(managedplatform.NeonComponents()), NeonIdentities: neonIdentities, NeonProxyControlPlaneOrigin: "https://control.example.test", NeonControlPlaneNamespace: "hakopod-system", NeonControlPlanePodLabels: map[string]string{"app.kubernetes.io/name": "hakopod-server"}, ApprovedExternalHTTPSCIDRs: []string{"8.8.8.8/32"}, NeonProxyToken: strings.Repeat("t", 32)}
	if supabase, neon, err := validateManagedPlatformInventories(config); err != nil || supabase || !neon {
		t.Fatalf("Neon-only inventory was not accepted: supabase=%t neon=%t err=%v", supabase, neon, err)
	}
	config.NeonProxyControlPlaneOrigin = "https://attacker.example.test/path"
	if _, _, err := validateManagedPlatformInventories(config); err == nil || !strings.Contains(err.Error(), "exact HTTPS origin") {
		t.Fatalf("unsafe Neon proxy control-plane origin was accepted: %v", err)
	}
	config.NeonProxyControlPlaneOrigin = "https://control.example.test"
	config.NeonControlPlanePodLabels = nil
	if _, _, err := validateManagedPlatformInventories(config); err == nil || !strings.Contains(err.Error(), "network trust") {
		t.Fatalf("unscoped Neon control-plane ingress was accepted: %v", err)
	}
}
