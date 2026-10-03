package platformconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/pelletier/go-toml/v2"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
)

func TestSelfHostedCapacitySchedulingReachesDurableReview(t *testing.T) {
	dsn := os.Getenv("HAKOPOD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set HAKOPOD_TEST_DATABASE_URL for isolated real PostgreSQL configuration checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect to isolated test PostgreSQL")
	}
	name := "hakopod_config_test_" + store.NewID()
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	var db *store.Store
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error("remove isolated configuration test database")
		}
		_ = admin.Close(cleanup)
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid test PostgreSQL URL")
	}
	parsed.Path = "/" + name
	db, err = store.Open(ctx, parsed.String())
	if err != nil {
		t.Fatal("open isolated configuration database")
	}
	var actualDatabase string
	if err = db.Pool.QueryRow(ctx, "SELECT current_database()").Scan(&actualDatabase); err != nil || actualDatabase != name {
		t.Fatal("configuration test connection did not select its new isolated database")
	}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	token, err := db.Bootstrap(ctx, "configuration-fixture")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := db.Authenticate(ctx, token)
	if err != nil {
		t.Fatal("authenticate isolated fixture")
	}

	policy := managedplatform.CapacityPolicy{Enabled: true, Pool: "operator-capacity", SchedulingPool: "operator-workloads", SchedulingRuntimeClass: "runsc", StorageClass: "encrypted", Capacity: managedplatform.Capacity{CPUMilli: 16000, MemoryBytes: 32 << 30, StorageGiB: 100}, Nodes: []managedplatform.CapacityNode{{Name: "worker", UID: "worker-uid", Architecture: "amd64", OperatingSystem: "linux"}}}
	parameters := map[string]string{"skuName": "Premium_LRS"}
	encodedParameters, _ := json.Marshal(parameters)
	parametersDigest := sha256.Sum256(encodedParameters)
	images := managedplatform.SupabaseReleaseImages()
	config := managedPlatformFile{SchemaVersion: 1, ApprovedEncryptedStorageClass: "encrypted", Images: images, Identities: map[string]managedplatform.RuntimeIdentity{}, Capacity: policy, SupabaseQualification: &cluster.SupabaseOperatorBinding{
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

	// This fixture serves only Kubernetes reads; native acceptance checks actual scheduling.
	namespace := &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID("cluster-uid")}}
	storageClass := &storagev1.StorageClass{TypeMeta: metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "StorageClass"}, ObjectMeta: metav1.ObjectMeta{Name: "encrypted", UID: types.UID("storage-uid")}, Provisioner: "disk.csi.azure.com", Parameters: parameters}
	node := &corev1.Node{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"}, ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: types.UID("worker-uid"), Labels: map[string]string{"hakopod.com/pool": policy.SchedulingPool}}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: "hakopod.com/pool", Value: policy.SchedulingPool, Effect: corev1.TaintEffectNoSchedule}}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64", OperatingSystem: "linux"}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("32"), corev1.ResourceMemory: resource.MustParse("64Gi")}}}
	runtime := &nodev1.RuntimeClass{TypeMeta: metav1.TypeMeta{APIVersion: "node.k8s.io/v1", Kind: "RuntimeClass"}, ObjectMeta: metav1.ObjectMeta{Name: "runsc", UID: types.UID("runtime-uid")}, Handler: "runsc"}
	kubernetes := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Error("configuration test attempted Kubernetes mutation")
			http.Error(response, "read-only fixture", http.StatusMethodNotAllowed)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/namespaces/kube-system":
			_ = json.NewEncoder(response).Encode(namespace)
		case "/apis/storage.k8s.io/v1/storageclasses/encrypted":
			_ = json.NewEncoder(response).Encode(storageClass)
		case "/api/v1/nodes/worker":
			_ = json.NewEncoder(response).Encode(node)
		case "/apis/node.k8s.io/v1/runtimeclasses/runsc":
			_ = json.NewEncoder(response).Encode(runtime)
		case "/api/v1/pods":
			_ = json.NewEncoder(response).Encode(&corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []corev1.Pod{}})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(kubernetes.Close)
	kube, err := cluster.NewWithConfig(&rest.Config{Host: kubernetes.URL}, cluster.Options{})
	if err != nil {
		t.Fatal(err)
	}
	planner, _, _, err := Configure(path, db, kube, hex.EncodeToString(make([]byte, 32)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	spec := managedplatform.Spec{SchemaVersion: 1, Name: "configuration-fixture", Kind: "supabase", Version: managedplatform.SupabaseVersion, Resources: map[string]managedplatform.Resources{}, Storage: map[string]int64{"database": 20, "database-encryption": 1, "edge-functions": 1, "objects": 20, "studio-snippets": 1}, Secrets: map[string]managedplatform.SecretReference{}, Placement: managedplatform.Placement{NodeNames: []string{"worker"}}, Supabase: &managedplatform.SupabaseConfig{PublicURL: "https://data.example.test", SiteURL: "https://app.example.test", RedirectURLs: []string{"https://app.example.test"}, DatabaseName: "postgres", JWTExpirySeconds: 3600, RESTMaxRows: 1000, StorageFileLimitBytes: 50 << 20, PoolSize: 20, PoolMaxClients: 100}}
	for _, name := range managedplatform.SupabaseComponentNames() {
		spec.Resources[name] = managedplatform.Resources{CPU: "250m", Memory: "512Mi"}
	}
	spec.Resources["database"] = managedplatform.Resources{CPU: "500m", Memory: "2Gi"}
	for _, name := range managedplatform.SupabaseRequiredSecretKeys() {
		spec.Secrets[name] = managedplatform.SecretReference{Name: "supabase-" + name, Revision: 1}
	}
	item := store.ManagedPlatform{ID: store.NewID(), Project: "demo", Environment: "development", Spec: spec}
	plan, err := planner.PlanManagedPlatform(ctx, principal, item, 0, "create")
	if err != nil {
		t.Fatal(err)
	}
	if err = policy.Allows(spec, plan); err != nil {
		t.Fatalf("configured self-hosted plan rejected by its own capacity policy: %v", err)
	}
	review, err := db.SaveManagedPlatformReview(ctx, principal, item, plan, 0, "create")
	if err != nil {
		t.Fatal(err)
	}
	if review.ID == "" || review.CapacityFingerprint == "" {
		t.Fatal("durable review did not bind the configured capacity policy")
	}
}
