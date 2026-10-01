package cluster

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/managedplatform"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestManagedClusterPinsEveryNodeUID(t *testing.T) {
	ctx := context.Background()
	c := cloudClient(2)
	for i, name := range []string{"node-0", "node-1"} {
		node, _ := c.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		node.UID = types.UID([]string{"uid-a", "uid-b"}[i])
		c.kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		c.options.ManagedClusterNodes = append(c.options.ManagedClusterNodes, ManagedClusterNode{Name: name, UID: string(node.UID)})
	}
	if err := c.ValidateCloudCapacity(ctx); err != nil {
		t.Fatal(err)
	}
	caps, err := c.CloudCapabilities(ctx)
	if err != nil || len(caps.ManagedClusterNodes) != 2 || caps.PublicTCP || caps.ContainerDaemon {
		t.Fatal(caps, err)
	}
	node, _ := c.kube.CoreV1().Nodes().Get(ctx, "node-0", metav1.GetOptions{})
	node.UID = "replacement"
	c.kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if c.ValidateCloudCapacity(ctx) == nil {
		t.Fatal("replacement node retained approval")
	}
	c.options.ManagedClusterNodes = nil
	if c.ValidateCloudCapacity(ctx) == nil {
		t.Fatal("default node limit relaxed")
	}
}

func TestManagedPlatformGrantCoversOnlyScheduledSupabaseTemplate(t *testing.T) {
	ctx := context.Background()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "uid-worker"}, Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("2Gi")}}}
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	name := "supabase-fixture"
	labels := map[string]string{managedBy: "hakopod", "hakopod.io/managed-platform-id": id, "hakopod.io/managed-platform": name, "app.kubernetes.io/name": "supabase-auth", "app.kubernetes.io/component": "auth"}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "managed-platform-" + id, UID: "owned-uid", Labels: labels}}
	replicas := int32(1)
	podTemplate := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{NodeSelector: map[string]string{corev1.LabelHostname: node.Name}, Containers: []corev1.Container{{Name: "auth", Image: "registry.example.test/supabase/auth@sha256:" + strings.Repeat("a", 64), Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")}}}}}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "supabase-auth", Namespace: namespace.Name, UID: "deployment-uid", Labels: labels}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}, Template: podTemplate}}
	replica := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "supabase-auth-rs", Namespace: namespace.Name, UID: "replicaset-uid", Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name, UID: deployment.UID}}}, Spec: appsv1.ReplicaSetSpec{Replicas: &replicas, Template: podTemplate}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: namespace.Name, UID: "pod-uid", Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: replica.Name, UID: replica.UID}}}, Spec: podTemplate.Spec}
	pod.Spec.NodeName = node.Name
	c := &Client{kube: fake.NewClientset(node, namespace, deployment, replica, pod)}
	workloadCapacity := managedplatform.Capacity{CPUMilli: 500, MemoryBytes: 512<<20 + managedplatform.PodMemoryOverheadBytes}
	ownership := managedplatform.CapacityNamespaceOwnership{PlatformID: id, PlatformName: name, UID: "owned-uid", Controllers: map[string]string{"deployment." + deployment.Name: string(deployment.UID)}, Workloads: map[string]managedplatform.CapacityWorkloadOwnership{"deployment." + deployment.Name: {UID: string(deployment.UID), Nodes: map[string]managedplatform.Capacity{node.Name: workloadCapacity}}}}
	reservation := ManagedPlatformNodeReservation{UID: "uid-worker", Capacity: managedplatform.Capacity{CPUMilli: 1300, MemoryBytes: 1280 << 20}, Namespaces: map[string]managedplatform.CapacityNamespaceOwnership{namespace.Name: ownership}}
	if err := c.CheckManagedPlatformNodeReservations(ctx, map[string]ManagedPlatformNodeReservation{node.Name: reservation}); err != nil {
		t.Fatal(err)
	}
	spoof := pod.DeepCopy()
	spoof.Name, spoof.UID, spoof.OwnerReferences = "unowned", "unowned-pod-uid", nil
	if _, err := c.kube.CoreV1().Pods(namespace.Name).Create(ctx, spoof, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckManagedPlatformNodeReservations(ctx, map[string]ManagedPlatformNodeReservation{node.Name: reservation}); err == nil {
		t.Fatal("unclaimed pod in an owned namespace was excluded from external workload usage")
	}
	if err := c.kube.CoreV1().Pods(namespace.Name).Delete(ctx, spoof.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	extra := pod.DeepCopy()
	extra.Name, extra.UID = "platform-extra", "extra-pod-uid"
	if _, err := c.kube.CoreV1().Pods(namespace.Name).Create(ctx, extra, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckManagedPlatformNodeReservations(ctx, map[string]ManagedPlatformNodeReservation{node.Name: reservation}); err == nil {
		t.Fatal("extra pod from a claimed controller disappeared from capacity accounting")
	}
	if err := c.kube.CoreV1().Pods(namespace.Name).Delete(ctx, extra.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	mutated := pod.DeepCopy()
	mutated.Name, mutated.UID = "platform-mutated", "mutated-pod-uid"
	mutated.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("750m")
	if _, err := c.kube.CoreV1().Pods(namespace.Name).Create(ctx, mutated, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckManagedPlatformNodeReservations(ctx, map[string]ManagedPlatformNodeReservation{node.Name: reservation}); err == nil {
		t.Fatal("pod that differs from its claimed controller template disappeared from capacity accounting")
	}
}
func TestManagedClusterFileIsStrictAndBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.toml")
	valid := "schema_version=1\n[[nodes]]\nname='node-a'\nuid='uid-a'\n"
	for _, value := range []string{valid, "schema_version=true\n", valid + "unknown=true\n", valid + "[[nodes]]\nname='node-a'\nuid='other'\n"} {
		os.WriteFile(path, []byte(value), 0600)
		_, err := ReadManagedClusterNodes(path)
		if (err == nil) != (value == valid) {
			t.Fatal("unexpected parse result", err)
		}
	}
	link := path + "-link"
	os.Symlink(path, link)
	if _, err := ReadManagedClusterNodes(link); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestDatabaseGrantAccountsForExistingPodsAndHeadroom(t *testing.T) {
	ctx := context.Background()
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "uid-worker"}, Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("2Gi")}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "default"}, Spec: corev1.PodSpec{NodeName: "worker", Containers: []corev1.Container{{Name: "other", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")}}}}}}
	c := &Client{kube: fake.NewClientset(&node, pod)}
	r := DatabaseNodeReservation{UID: "uid-worker", Capacity: database.Capacity{CPUMilli: 1000, MemoryBytes: 1024 << 20}, Scopes: []string{"owned/production"}}
	if err := c.checkDatabaseNodeReservation(ctx, node, r); err != nil {
		t.Fatal(err)
	}
	r.Capacity.CPUMilli = 1500
	if c.checkDatabaseNodeReservation(ctx, node, r) == nil {
		t.Fatal("existing pod CPU ignored")
	}
	r.Capacity.CPUMilli = 1000
	r.Capacity.MemoryBytes = 1536 << 20
	if c.checkDatabaseNodeReservation(ctx, node, r) == nil {
		t.Fatal("existing pod memory or headroom ignored")
	}
	r.Capacity.MemoryBytes = 1024 << 20
	r.UID = "replaced"
	if c.checkDatabaseNodeReservation(ctx, node, r) == nil {
		t.Fatal("replacement UID accepted")
	}
}
func TestDatabaseDiscoveryDoesNotUseApplicationPlacement(t *testing.T) {
	ctx := context.Background()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "database-worker", Labels: map[string]string{"hakopod.com/pool": "database", DatabaseDefaultRuntimeLabel: "runsc", corev1.LabelTopologyZone: "observed-zone", corev1.LabelArchStable: "amd64"}}, Spec: corev1.NodeSpec{ProviderID: "azure://fixture"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	c := &Client{kube: fake.NewClientset(node), options: Options{DatabasePlacementPolicy: func(_ context.Context, p, e string) (DatabasePolicy, error) {
		if p != "owned" || e != "production" {
			t.Fatal("scope changed")
		}
		return DatabasePolicy{NodeName: node.Name, Pool: "database", RuntimeClass: "runsc"}, nil
	}, PlacementPolicy: func(context.Context, PlacementRequest) (WorkloadPolicy, error) {
		t.Fatal("application placement was used")
		return WorkloadPolicy{}, nil
	}}}
	items, err := c.DatabasePlacementNodes(ctx, "owned", "production")
	if err != nil || len(items) != 1 || items[0].Name != node.Name || items[0].Zone != "observed-zone" || items[0].Provider != "azure" || !items[0].Available {
		t.Fatal(items, err)
	}
}

func TestDatabasePlacementMixedArchitectureUsesApprovedSubset(t *testing.T) {
	ctx := context.Background()
	kube := fake.NewClientset()
	for name, arch := range map[string]string{"approved-arm": "arm64", "approved-amd": "amd64", "outside-amd": "amd64"} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{corev1.LabelArchStable: arch, corev1.LabelHostname: name}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
		if _, err := kube.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	c := &Client{kube: kube}
	policy := &DatabasePolicy{NodeNames: []string{"approved-arm", "approved-amd"}}
	for _, engine := range []string{"mysql", "mongodb", "vitess"} {
		s := database.Spec{Engine: engine, Mode: "standalone", Shards: 1}
		if err := c.validateDatabasePlacementNodes(ctx, s, policy); err != nil {
			t.Fatal(engine, err)
		}
		s.Placement.NodeNames = []string{"approved-arm"}
		if c.validateDatabasePlacementNodes(ctx, s, policy) == nil {
			t.Fatal(engine, "explicit incompatible selection widened")
		}
		s.Placement.NodeNames = nil
		if c.validateDatabasePlacementNodes(ctx, s, &DatabasePolicy{NodeNames: []string{"approved-arm"}}) == nil {
			t.Fatal(engine, "unapproved amd64 worker used")
		}
		s.Placement.Spread = "nodes"
		s.Mode = "cluster"
		s.Replicas = 2
		if c.validateDatabasePlacementNodes(ctx, s, policy) == nil {
			t.Fatal(engine, "insufficient compatible domains accepted")
		}
	}
}
