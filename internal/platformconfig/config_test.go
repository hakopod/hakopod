package platformconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/api"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/pelletier/go-toml/v2"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
)

func TestUnconfiguredServerHasNoTypedNilRuntime(t *testing.T) {
	server := &api.Server{}
	if err := Attach(server, "", "", Options{}); err != nil {
		t.Fatal(err)
	}
	if server.ManagedPlatformPlanner != nil || server.ManagedPlatformRuntime != nil || server.NeonProxyAuthority != nil || server.ManagedPlatformRecoveryQualified || server.ManagedNeonRecoveryQualified {
		t.Fatal("unconfigured server exposed a runtime or recovery capability")
	}
}

func TestEmbeddingPreservesWorkspaceAdmissionAndCapacity(t *testing.T) {
	policy := managedplatform.CapacityPolicy{Enabled: true, Pool: "workspace", StorageClass: "encrypted", Capacity: managedplatform.Capacity{CPUMilli: 1000, MemoryBytes: 1 << 30, StorageGiB: 10}, Nodes: []managedplatform.CapacityNode{{Name: "worker", UID: "worker-uid", Architecture: "amd64", OperatingSystem: "linux"}}}
	db := &store.Store{RequireManagedPlatformAdmission: true}
	db.AdmitManagedPlatform = func(context.Context, pgx.Tx, store.Principal, string, string, string) error {
		return store.ErrForbidden
	}
	db.ManagedPlatformCapacityBudget = func(context.Context, pgx.Tx, string, string) (managedplatform.CapacityPolicy, error) {
		return policy, nil
	}
	db.ManagedCapacityPool = func(context.Context, pgx.Tx, string, string) (string, error) { return "workspace", nil }
	db.ValidateManagedPlatformCapacity = func(context.Context, string, string, managedplatform.CapacityPolicy) error { return store.ErrForbidden }
	parameters := map[string]string{"skuName": "Premium_LRS"}
	encodedParameters, err := json.Marshal(parameters)
	if err != nil {
		t.Fatal(err)
	}
	parametersDigest := sha256.Sum256(encodedParameters)
	images := managedplatform.SupabaseReleaseImages()
	config := managedPlatformFile{SchemaVersion: 1, ApprovedEncryptedStorageClass: "encrypted", Images: images, Identities: map[string]managedplatform.RuntimeIdentity{}, SupabaseQualification: &cluster.SupabaseOperatorBinding{
		SchemaVersion: 1, Release: managedplatform.SupabaseReleaseQualificationID, ClusterUID: "cluster-uid", StorageClassName: "encrypted", StorageClassUID: "storage-uid", StorageProvisioner: "disk.csi.azure.com", StorageParametersSHA256: hex.EncodeToString(parametersDigest[:]), ImageInventorySHA256: managedplatform.SupabaseImageInventorySHA256(images), ProviderEvidenceSHA256: strings.Repeat("c", 64), EncryptionAtRest: true, Reviewed: true,
	}}
	for _, name := range managedplatform.SupabaseComponentNames() {
		config.Identities[name] = managedplatform.RuntimeIdentity{UID: 10001, GID: 10001}
	}
	body, err := toml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := writeManagedPlatformConfig(t, string(body), 0600)
	options := Options{ExternalCapacity: true, CatalogCapacity: func(_ context.Context, project, environment string) (managedplatform.CapacityPolicy, error) {
		if project != "owned" || environment != "production" {
			return managedplatform.CapacityPolicy{}, store.ErrForbidden
		}
		return policy, nil
	}}
	namespace := &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID("cluster-uid")}}
	storageClass := &storagev1.StorageClass{TypeMeta: metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"}, ObjectMeta: metav1.ObjectMeta{Name: "encrypted", UID: types.UID("storage-uid")}, Provisioner: "disk.csi.azure.com", Parameters: parameters}
	kubernetes := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/namespaces/kube-system":
			_ = json.NewEncoder(response).Encode(namespace)
		case "/apis/storage.k8s.io/v1/storageclasses/encrypted":
			_ = json.NewEncoder(response).Encode(storageClass)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(kubernetes.Close)
	kube, err := cluster.NewWithConfig(&rest.Config{Host: kubernetes.URL}, cluster.Options{})
	if err != nil {
		t.Fatal(err)
	}
	planner, _, _, err := Configure(path, db, kube, hex.EncodeToString(make([]byte, 32)), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = planner.CatalogCapacity(context.Background(), "foreign", "production"); err != store.ErrForbidden {
		t.Fatal("catalog lost workspace scope", err)
	}
	if pool, err := db.ManagedCapacityPool(context.Background(), nil, "owned", "production"); err != nil || pool != "workspace" {
		t.Fatal("runtime replaced workspace capacity", pool, err)
	}
	if err := db.AdmitManagedPlatform(context.Background(), nil, store.Principal{}, "owned", "production", "create"); err != store.ErrForbidden {
		t.Fatal("runtime replaced admission", err)
	}
	config.Capacity = policy
	body, err = toml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path = writeManagedPlatformConfig(t, string(body), 0600)
	if _, _, _, err = Configure(path, db, kube, hex.EncodeToString(make([]byte, 32)), options); err == nil || !strings.Contains(err.Error(), "cannot override") {
		t.Fatal("accepted global capacity in Cloud config", err)
	}
}

func writeManagedPlatformConfig(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "managed-platforms.toml")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
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
	t.Run("versioned Supabase qualification", func(t *testing.T) {
		path := writeManagedPlatformConfig(t, `schema_version = 1
[supabase_qualification]
schema_version = 1
release = "supabase-0.8.2-linux-amd64"
cluster_uid = "cluster-uid"
storage_class_name = "encrypted-rwo"
storage_class_uid = "storage-uid"
storage_provisioner = "disk.csi.azure.com"
storage_parameters_sha256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
image_inventory_sha256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
provider_evidence_sha256 = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
encryption_at_rest = true
reviewed = true
`, 0600)
		config, err := loadManagedPlatformFile(path)
		if err != nil || config.SupabaseQualification == nil || config.SupabaseQualification.SchemaVersion != 1 {
			t.Fatalf("Supabase qualification was not decoded: %v", err)
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
	if _, _, _, err := Configure("configured", nil, nil, "", Options{}); err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
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
