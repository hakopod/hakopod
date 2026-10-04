package store

import (
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestSupabaseClaimAdmissionCoversExactRenderedInventory(t *testing.T) {
	spec := managedplatform.Spec{SchemaVersion: 1, Name: "claim-contract", Kind: "supabase", Version: managedplatform.SupabaseVersion, Resources: map[string]managedplatform.Resources{}, Storage: map[string]int64{}, Secrets: map[string]managedplatform.SecretReference{}, Placement: managedplatform.Placement{NodeNames: []string{"node-a"}}, Supabase: &managedplatform.SupabaseConfig{PublicURL: "https://supabase.example.test", SiteURL: "https://app.example.test", DatabaseName: "postgres", JWTExpirySeconds: 3600, RESTMaxRows: 1000, StorageFileLimitBytes: 10 << 20, PoolSize: 10, PoolMaxClients: 100}}
	images := map[string]string{}
	identities := map[string]managedplatform.RuntimeIdentity{}
	for _, name := range managedplatform.SupabaseComponentNames() {
		spec.Resources[name] = managedplatform.Resources{CPU: "500m", Memory: "2Gi"}
		images[name] = "registry.example.test/supabase/" + name + "@sha256:" + strings.Repeat("a", 64)
		identities[name] = managedplatform.RuntimeIdentity{UID: 10000, GID: 10000}
	}
	identities["database"] = managedplatform.RuntimeIdentity{UID: 100, GID: 101}
	for _, name := range managedplatform.SupabaseRequiredStorageKeys() {
		spec.Storage[name] = 20
	}
	for _, name := range managedplatform.SupabaseRequiredSecretKeys() {
		spec.Secrets[name] = managedplatform.SecretReference{Name: "supabase-" + name, Revision: 1}
	}
	plan, err := managedplatform.PlanSupabase(spec, images)
	if err != nil {
		t.Fatal(err)
	}
	platformID := strings.Repeat("a", 32)
	plan.Namespace = "managed-platform-" + platformID
	assets, err := managedplatform.PinnedSupabaseAssets()
	if err != nil {
		t.Fatal(err)
	}
	manifests, err := managedplatform.RenderSupabase(managedplatform.SupabaseRenderInput{Spec: spec, PlatformID: platformID, Images: images, Revision: 1, NamespaceUID: types.UID("namespace-uid"), Assets: assets, Identities: identities, ApprovedEncryptedStorageClass: "encrypted-rwo", DatabaseClaim: managedplatform.ObservedClaimState{Observed: true}, SharedStorageGID: 20000, ApprovedExternalHTTPSCIDRs: []string{"8.8.8.8/32"}})
	if err != nil {
		t.Fatal(err)
	}
	op := ManagedPlatformOperation{ID: strings.Repeat("b", 32), PlatformID: platformID, Revision: 1, Kind: "create", Spec: spec, Plan: plan}
	assertAllowed := func(component string) {
		t.Helper()
		if !managedPlatformClaimComponentAllowed(op, component, "runtime_component") {
			t.Fatalf("rendered resource %q is outside claim admission", component)
		}
	}
	assertAllowed("namespace." + manifests.Namespace.Name)
	assertAllowed("configmap.supabase-database-credentials-r1")
	for _, name := range manifests.RequiredSecrets {
		assertAllowed("secret." + name)
	}
	for _, object := range manifests.Objects {
		var kind, name string
		switch item := object.(type) {
		case *corev1.ConfigMap:
			kind, name = "configmap", item.Name
		case *corev1.PersistentVolumeClaim:
			kind, name = "pvc", item.Name
		case *corev1.Service:
			kind, name = "service", item.Name
		case *appsv1.Deployment:
			kind, name = "deployment", item.Name
		case *appsv1.StatefulSet:
			kind, name = "statefulset", item.Name
		case *networkingv1.NetworkPolicy:
			kind, name = "networkpolicy", item.Name
		default:
			t.Fatalf("renderer returned an unclassified resource %T", object)
		}
		assertAllowed(kind + "." + name)
	}
	if managedPlatformClaimComponentAllowed(op, "networkpolicy.supabase-invented-internal", "runtime_component") {
		t.Fatal("invented component policy crossed claim admission")
	}
}
