package cluster

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

type neonClaimReadStore struct {
	ManagedPlatformOperationStore
	claims map[int64][]store.PlatformResourceClaim
}

func (s neonClaimReadStore) PlatformResourceClaims(_ context.Context, _ store.ManagedPlatformOperation, revision int64) ([]store.PlatformResourceClaim, error) {
	return append([]store.PlatformResourceClaim(nil), s.claims[revision]...), nil
}

func TestNeonColdStartSeparatesBootstrapAndServingReadiness(t *testing.T) {
	replicas := int32(1)
	compute := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "neon-compute-0"}, Spec: appsv1.StatefulSetSpec{Replicas: &replicas}, Status: appsv1.StatefulSetStatus{ObservedGeneration: 1, UpdatedReplicas: 1, CurrentRevision: "r1", UpdateRevision: "r1"}}
	compute.Generation = 1
	if !neonStatefulSetObserved(compute, false) || neonStatefulSetObserved(compute, true) || neonBootstrapRequiresReadiness(compute.Name) {
		t.Fatal("detached compute did not pass bootstrap while remaining unready for serving")
	}
	pageserver := compute.DeepCopy()
	pageserver.Name = "neon-pageserver-0"
	if !neonStatefulSetObserved(pageserver, false) || neonStatefulSetObserved(pageserver, true) || neonBootstrapRequiresReadiness(pageserver.Name) {
		t.Fatal("unattached pageserver did not pass bootstrap while remaining unready for serving")
	}
	controller := compute.DeepCopy()
	controller.Name = "neon-controller-database"
	if !neonBootstrapRequiresReadiness(controller.Name) || neonStatefulSetObserved(controller, true) || neonStatefulSetObserved(controller, neonBootstrapRequiresReadiness(controller.Name)) {
		t.Fatal("controller database bypassed bootstrap readiness")
	}
	proxy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "neon-proxy"}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 1}}
	proxy.Generation = 1
	if !neonDeploymentObserved(proxy, false) || neonDeploymentObserved(proxy, true) || neonBootstrapRequiresReadiness(proxy.Name) {
		t.Fatal("proxy readiness phases are not separated")
	}
}

func TestNeonAvailabilityZonesComeFromReadyKubernetesNodes(t *testing.T) {
	node := func(name, zone string) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"topology.kubernetes.io/zone": zone}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	}
	client := &Client{kube: fake.NewSimpleClientset(node("node-a", "zone-a"), node("node-b", "zone-b"), node("node-c", "zone-c"))}
	spec := managedplatform.Spec{Placement: managedplatform.Placement{NodeNames: []string{"node-a", "node-b", "node-c"}}, Neon: &managedplatform.NeonConfig{Pageservers: 2}}
	zones, err := client.neonAvailabilityZones(context.Background(), spec)
	if err != nil || strings.Join(zones, ",") != "zone-a,zone-b,zone-c" {
		t.Fatalf("trusted node zones were not resolved: %v %v", zones, err)
	}
	missing := &Client{kube: fake.NewSimpleClientset(node("node-a", "zone-a"), node("node-b", "zone-b"), node("node-c", ""))}
	if _, err = missing.neonAvailabilityZones(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "trusted availability-zone identity") {
		t.Fatalf("missing node zone was accepted: %v", err)
	}
}

func TestNeonDeleteDoesNotDependOnLiveNodeReadiness(t *testing.T) {
	client := &Client{kube: fake.NewSimpleClientset()}
	spec := managedplatform.Spec{Placement: managedplatform.Placement{NodeNames: []string{"missing-a", "missing-b", "missing-c"}}, Neon: &managedplatform.NeonConfig{Pageservers: 2}}
	zones, err := client.neonLifecycleZones(context.Background(), "delete", spec)
	if err != nil || len(zones) != 0 {
		t.Fatalf("delete unexpectedly depended on live placement nodes: %v %v", zones, err)
	}
	if _, err = client.neonLifecycleZones(context.Background(), "create", spec); err == nil {
		t.Fatal("create bypassed trusted live availability-zone discovery")
	}
}

func TestNeonClaimLoadingSeparatesProviderLifecycleFromKubernetesReconcile(t *testing.T) {
	op := store.ManagedPlatformOperation{ID: "operation", PlatformID: strings.Repeat("4", 32), Revision: 2, Kind: "update", Spec: managedplatform.Spec{Kind: "neon"}}
	fixture := neonClaimReadStore{claims: map[int64][]store.PlatformResourceClaim{
		1: {
			{PlatformID: op.PlatformID, PlatformRevision: 1, Component: "configmap.neon-pageserver-0-r1", Kind: "runtime_component", ResourceID: "configmap-uid", ImmutableGeneration: 1, OwnerOperationID: "prior-operation"},
			{PlatformID: op.PlatformID, PlatformRevision: 1, Component: "tenant", Kind: "neon_tenant", ResourceID: "tenant@1", ImmutableGeneration: 1, OwnerOperationID: "prior-operation"},
			{PlatformID: op.PlatformID, PlatformRevision: 1, Component: "compute-compute-0", Kind: "runtime_component", ResourceID: "compute", ImmutableGeneration: 1, OwnerOperationID: "prior-operation"},
		},
		2: {
			{PlatformID: op.PlatformID, PlatformRevision: 2, Component: "service.neon-proxy", Kind: "runtime_component", ResourceID: "service-uid", ImmutableGeneration: 1, OwnerOperationID: op.ID},
			{PlatformID: op.PlatformID, PlatformRevision: 2, Component: "timeline", Kind: "neon_timeline", ResourceID: "timeline", ImmutableGeneration: 1, OwnerOperationID: op.ID},
		},
	}}
	prior, current, err := loadSupabaseClaims(context.Background(), fixture, op)
	if err != nil || len(prior) != 1 || prior["configmap.neon-pageserver-0-r1"].ResourceID == "" || len(current) != 1 || current["service.neon-proxy"].ResourceID == "" {
		t.Fatalf("Neon update did not isolate Kubernetes claims from provider lifecycle claims: %v %#v %#v", err, prior, current)
	}
	op.Kind = "delete"
	prior, current, err = loadSupabaseClaims(context.Background(), fixture, op)
	if err != nil || len(prior) != 3 || len(current) != 2 || prior["tenant"].Kind != "neon_tenant" || current["timeline"].Kind != "neon_timeline" {
		t.Fatalf("Neon delete lost provider claims before namespace release: %v %#v %#v", err, prior, current)
	}
	fixture.claims[2][1].Kind = "runtime_component"
	if _, _, err = loadSupabaseClaims(context.Background(), fixture, op); err == nil {
		t.Fatal("Neon lifecycle claim with the wrong kind was accepted")
	}
}

func TestManagedPlatformClaimReaderAllowsBoundedNeonRevisionRollover(t *testing.T) {
	if maxSupabaseRuntimeObjects != managedplatform.MaxComponents*5 || maxSupabaseRuntimeObjects < 135 {
		t.Fatal("managed platform reconcile bound does not cover Neon revision rollover")
	}
	op := store.ManagedPlatformOperation{ID: "operation", PlatformID: strings.Repeat("5", 32), Revision: 2, Kind: "delete", Spec: managedplatform.Spec{Kind: "neon"}}
	fixture := neonClaimReadStore{claims: map[int64][]store.PlatformResourceClaim{1: {}, 2: {}}}
	for i := 0; i < 14; i++ {
		fixture.claims[1] = append(fixture.claims[1], store.PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 1, Component: fmt.Sprintf("configmap.prior-%d", i), Kind: "runtime_component", ResourceID: fmt.Sprintf("prior-%d", i), ImmutableGeneration: 1, OwnerOperationID: "prior-operation"})
	}
	for i := 0; i < 121; i++ {
		fixture.claims[2] = append(fixture.claims[2], store.PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 2, Component: fmt.Sprintf("service.current-%d", i), Kind: "runtime_component", ResourceID: fmt.Sprintf("current-%d", i), ImmutableGeneration: 1, OwnerOperationID: op.ID})
	}
	if prior, current, err := loadSupabaseClaims(context.Background(), fixture, op); err != nil || len(prior)+len(current) != 135 {
		t.Fatalf("valid 135-resource Neon rollover was rejected: %v", err)
	}
	for i := 121; i < 147; i++ {
		fixture.claims[2] = append(fixture.claims[2], store.PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 2, Component: fmt.Sprintf("service.current-%d", i), Kind: "runtime_component", ResourceID: fmt.Sprintf("current-%d", i), ImmutableGeneration: 1, OwnerOperationID: op.ID})
	}
	if _, _, err := loadSupabaseClaims(context.Background(), fixture, op); err == nil {
		t.Fatal("managed platform claim reader accepted more than 160 active resources")
	}
}

func TestManagedPlatformNeonRendererInventoryFitsClaimBounds(t *testing.T) {
	render := func(t *testing.T, pageservers, safekeepers, computes int) managedplatform.NeonManifests {
		t.Helper()
		spec := managedplatform.Spec{SchemaVersion: 1, Name: "inventory", Kind: "neon", Version: managedplatform.NeonVersion, Resources: map[string]managedplatform.Resources{}, Storage: map[string]int64{}, Secrets: map[string]managedplatform.SecretReference{}, Neon: &managedplatform.NeonConfig{PostgresVersion: "17", ComputeReplicas: computes, Pageservers: pageservers, Safekeepers: safekeepers, BranchLimit: 64, ObjectStorageURL: "https://objects.example.test", ObjectStorageBucket: "neon-inventory", ObjectStorageRegion: "us-east-1", ObjectStoragePrefix: "inventory", ProxyControlPlanePatchSHA256: managedplatform.NeonProxyControlPlanePatchSHA256}}
		for i := 0; i < max(3, pageservers); i++ {
			spec.Placement.NodeNames = append(spec.Placement.NodeNames, fmt.Sprintf("node-%d", i))
		}
		images := map[string]string{}
		identities := map[string]managedplatform.NeonRuntimeIdentity{}
		for _, component := range managedplatform.NeonComponents() {
			spec.Resources[component] = managedplatform.Resources{CPU: "500m", Memory: "2Gi"}
			images[component] = "registry.example.test/neon/" + component + "@sha256:" + strings.Repeat("a", 64)
			identities[component] = managedplatform.NeonRuntimeIdentity{UID: 10001, GID: 10001}
		}
		for _, key := range managedplatform.NeonStorageKeys() {
			spec.Storage[key] = 20
		}
		for _, key := range managedplatform.NeonSecretKeys() {
			spec.Secrets[key] = managedplatform.SecretReference{Name: "neon-" + key, Revision: 1}
		}
		manifests, err := managedplatform.RenderNeon(managedplatform.NeonRenderInput{Spec: spec, PlatformID: strings.Repeat("a", 32), Revision: 1, NamespaceUID: types.UID("namespace-uid"), Images: images, Identities: identities, ApprovedEncryptedStorageClass: "encrypted", SharedStorageGID: 20000, ProxyControlPlaneOrigin: "https://control.example.test", ControlPlaneNamespace: "hakopod-system", ControlPlanePodLabels: map[string]string{"app.kubernetes.io/name": "hakopod-server"}, ApprovedExternalHTTPSCIDRs: []string{"8.8.8.8/32"}})
		if err != nil {
			t.Fatal(err)
		}
		return manifests
	}
	assert := func(t *testing.T, pageservers, safekeepers, computes, wantObjects, wantDelete int) {
		t.Helper()
		manifests := render(t, pageservers, safekeepers, computes)
		kubernetesClaims := len(manifests.Objects) + len(manifests.RequiredSecrets) + 1
		lifecycleClaims := 2 + pageservers + safekeepers + computes
		if len(manifests.Objects) != wantObjects || kubernetesClaims+lifecycleClaims != wantDelete {
			t.Fatalf("renderer-derived inventory changed: objects=%d Kubernetes=%d lifecycle=%d delete=%d", len(manifests.Objects), kubernetesClaims, lifecycleClaims, kubernetesClaims+lifecycleClaims)
		}
		if kubernetesClaims+lifecycleClaims > maxSupabaseRuntimeObjects {
			t.Fatal("supported Neon topology exceeds the managed platform claim bound")
		}
	}
	assert(t, 2, 3, 1, 45, 61)
	assert(t, 8, 3, 6, 94, 121)

	maximum := render(t, 8, 3, 6)
	immutableRevisionObjects := 0
	for _, object := range maximum.Objects {
		if config, ok := object.(*corev1.ConfigMap); ok && strings.HasSuffix(config.Name, "-r1") {
			immutableRevisionObjects++
		}
	}
	kubernetesClaims := len(maximum.Objects) + len(maximum.RequiredSecrets) + 1
	if immutableRevisionObjects != 14 || kubernetesClaims+immutableRevisionObjects != 116 {
		t.Fatalf("maximum update rollover inventory changed: Kubernetes=%d old immutable ConfigMaps=%d total=%d", kubernetesClaims, immutableRevisionObjects, kubernetesClaims+immutableRevisionObjects)
	}
	if maxSupabaseRuntimeObjects != managedplatform.MaxComponents*5 {
		t.Fatalf("managed platform claim bound is not derived from the component bound: %d", maxSupabaseRuntimeObjects)
	}
	if maxSupabaseRuntimeObjects != 160 {
		t.Fatalf("managed platform claim bound changed: %d", maxSupabaseRuntimeObjects)
	}
}
