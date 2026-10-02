package cluster

import (
	"context"
	"math"
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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "uid-worker"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64", OperatingSystem: "linux"}, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("2Gi")}}}
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	name := "supabase-fixture"
	labels := map[string]string{managedBy: "hakopod", "hakopod.io/managed-platform-id": id, "hakopod.io/managed-platform": name, "app.kubernetes.io/name": "supabase-auth", "app.kubernetes.io/component": "auth"}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "managed-platform-" + id, UID: "owned-uid", Labels: labels}}
	replicas := int32(1)
	podTemplate := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{NodeSelector: map[string]string{corev1.LabelHostname: node.Name}, Containers: []corev1.Container{{Name: "auth", Image: "registry.example.test/supabase/auth@sha256:" + strings.Repeat("a", 64), Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")}}}}}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "supabase-auth", Namespace: namespace.Name, UID: "deployment-uid", Labels: labels}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}, Template: podTemplate}}
	replica := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "supabase-auth-rs", Namespace: namespace.Name, UID: "replicaset-uid", Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name, UID: deployment.UID, Controller: ptr(true)}}}, Spec: appsv1.ReplicaSetSpec{Replicas: &replicas, Template: podTemplate}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "platform", Namespace: namespace.Name, UID: "pod-uid", Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: replica.Name, UID: replica.UID, Controller: ptr(true)}}}, Spec: podTemplate.Spec}
	pod.Spec.NodeName = node.Name
	c := &Client{kube: fake.NewClientset(node, namespace, deployment, replica, pod)}
	workloadCapacity := managedplatform.Capacity{CPUMilli: 500, MemoryBytes: 512<<20 + managedplatform.PodMemoryOverheadBytes}
	ownership := managedplatform.CapacityNamespaceOwnership{PlatformID: id, PlatformName: name, UID: "owned-uid", Controllers: map[string]string{"deployment." + deployment.Name: string(deployment.UID)}, Workloads: map[string]managedplatform.CapacityWorkloadOwnership{"deployment." + deployment.Name: {UID: string(deployment.UID), Nodes: map[string]managedplatform.Capacity{node.Name: workloadCapacity}}}}
	for _, flag := range []*bool{nil, ptr(false)} {
		uncontrolled := pod.DeepCopy()
		uncontrolled.OwnerReferences[0].Controller = flag
		if _, _, owned, err := c.managedPlatformPodReservation(ctx, *uncontrolled, namespace, ownership); err != nil || owned {
			t.Fatal("non-controller Pod reference claimed a deployment reservation", err)
		}
		uncontrolledReplica := replica.DeepCopy()
		uncontrolledReplica.OwnerReferences[0].Controller = flag
		if _, err := c.kube.AppsV1().ReplicaSets(namespace.Name).Update(ctx, uncontrolledReplica, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, _, owned, err := c.managedPlatformPodReservation(ctx, *pod, namespace, ownership); err != nil || owned {
			t.Fatal("non-controller ReplicaSet reference claimed a deployment reservation", err)
		}
	}
	if _, err := c.kube.AppsV1().ReplicaSets(namespace.Name).Update(ctx, replica, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	set := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "supabase-database", Namespace: namespace.Name, UID: "statefulset-uid", Labels: labels}, Spec: appsv1.StatefulSetSpec{Replicas: &replicas, Template: podTemplate}}
	if _, err := c.kube.AppsV1().StatefulSets(namespace.Name).Create(ctx, set, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	setPod := pod.DeepCopy()
	setPod.Name = set.Name + "-0"
	setPod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: set.Name, UID: set.UID, Controller: ptr(true)}}
	ownership.Workloads["statefulset."+set.Name] = managedplatform.CapacityWorkloadOwnership{UID: string(set.UID), Nodes: map[string]managedplatform.Capacity{node.Name: workloadCapacity}}
	if _, _, owned, err := c.managedPlatformPodReservation(ctx, *setPod, namespace, ownership); err != nil || !owned {
		t.Fatal("exact StatefulSet controller reference was rejected", err)
	}
	for _, flag := range []*bool{nil, ptr(false)} {
		setPod.OwnerReferences[0].Controller = flag
		if _, _, owned, err := c.managedPlatformPodReservation(ctx, *setPod, namespace, ownership); err != nil || owned {
			t.Fatal("non-controller Pod reference claimed a StatefulSet reservation", err)
		}
	}
	reservation := ManagedPlatformNodeReservation{UID: "uid-worker", Architecture: "amd64", OperatingSystem: "linux", Capacity: managedplatform.Capacity{CPUMilli: 1300, MemoryBytes: 1280 << 20}, Namespaces: map[string]managedplatform.CapacityNamespaceOwnership{namespace.Name: ownership}}
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

func TestManagedPlatformGrantDoesNotDoubleCountPoolWorkloads(t *testing.T) {
	ctx := context.Background()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "uid-worker"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64", OperatingSystem: "linux"}, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("2Gi")}}}
	applicationID := "application-fixture"
	applicationOwner := ownerID(applicationID)
	applicationNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(applicationID), UID: "application-namespace", Labels: map[string]string{managedBy: "hakopod", ownerKey: applicationOwner, scopeKey: scopeLabel("project", "production")}}}
	applicationLabels := map[string]string{managedBy: "hakopod", ownerKey: applicationOwner, serviceKey: "application"}
	applicationDeployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "application", Namespace: applicationNamespace.Name, UID: "application-deployment", Labels: applicationLabels}}
	applicationReplica := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "application-rs", Namespace: applicationNamespace.Name, UID: "application-replica", Labels: applicationLabels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: applicationDeployment.Name, UID: applicationDeployment.UID, Controller: ptr(true)}}}}
	applicationPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "application", Namespace: applicationNamespace.Name, UID: "application-pod", Labels: applicationLabels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: applicationReplica.Name, UID: applicationReplica.UID, Controller: ptr(true)}}}, Spec: corev1.PodSpec{NodeName: node.Name, Containers: []corev1.Container{{Name: "application", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")}}}}}}
	databaseID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	databaseNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(databaseID), UID: "database-namespace", Labels: map[string]string{managedBy: "hakopod", databaseOwner: databaseID, "hakopod.io/project": "project", "hakopod.io/environment": "production"}}}
	databaseLabels := map[string]string{managedBy: "hakopod", databaseOwner: databaseID}
	databaseSet := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: databaseNamespace.Name, UID: "database-statefulset", Labels: databaseLabels}}
	databasePod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: databaseNamespace.Name, UID: "database-pod", Labels: databaseLabels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: databaseSet.Name, UID: databaseSet.UID, Controller: ptr(true)}}}, Spec: corev1.PodSpec{NodeName: node.Name, Containers: []corev1.Container{{Name: "database", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")}}}}}}
	c := &Client{kube: fake.NewClientset(node, applicationNamespace, applicationDeployment, applicationReplica, applicationPod, databaseNamespace, databaseSet, databasePod)}
	workloadCapacity := managedplatform.Capacity{CPUMilli: 500, MemoryBytes: 512 << 20}
	ownership := managedplatform.CapacityPoolOwnership{Workloads: []managedplatform.CapacityPoolWorkload{{Kind: "application", ID: applicationID, Project: "project", Environment: "production", Capacity: workloadCapacity}, {Kind: "database", ID: databaseID, Project: "project", Environment: "production", Capacity: workloadCapacity}}}
	reservation := ManagedPlatformNodeReservation{UID: "uid-worker", Architecture: "amd64", OperatingSystem: "linux", Capacity: managedplatform.Capacity{CPUMilli: 1300, MemoryBytes: 1 << 30}, Ownership: ownership}
	if err := c.CheckManagedPlatformNodeReservations(ctx, map[string]ManagedPlatformNodeReservation{node.Name: reservation}); err != nil {
		t.Fatal(err)
	}
	databaseReservation := DatabaseNodeReservation{UID: "uid-worker", Capacity: database.Capacity{CPUMilli: 1300, MemoryBytes: 1 << 30}, Scopes: []string{"project/production"}, Ownership: ownership}
	if err := c.CheckDatabaseNodeReservations(ctx, map[string]DatabaseNodeReservation{node.Name: databaseReservation}); err != nil {
		t.Fatal(err)
	}
	spoof := applicationPod.DeepCopy()
	spoof.Name, spoof.Labels = "unowned", map[string]string{managedBy: "hakopod"}
	if _, err := c.kube.CoreV1().Pods(applicationNamespace.Name).Create(ctx, spoof, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckManagedPlatformNodeReservations(ctx, map[string]ManagedPlatformNodeReservation{node.Name: reservation}); err == nil {
		t.Fatal("unowned application pod disappeared from external workload accounting")
	}
	if err := c.CheckDatabaseNodeReservations(ctx, map[string]DatabaseNodeReservation{node.Name: databaseReservation}); err == nil {
		t.Fatal("database-node accounting ignored an unowned application pod")
	}
	if err := c.kube.CoreV1().Pods(applicationNamespace.Name).Delete(ctx, spoof.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	foreign := applicationPod.DeepCopy()
	foreign.Name, foreign.UID = "foreign-controller", "foreign-controller-pod"
	foreign.OwnerReferences[0].UID = "foreign-replica"
	if _, err := c.kube.CoreV1().Pods(applicationNamespace.Name).Create(ctx, foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckManagedPlatformNodeReservations(ctx, map[string]ManagedPlatformNodeReservation{node.Name: reservation}); err == nil {
		t.Fatal("pod with a foreign controller UID disappeared from capacity accounting")
	}
	if err := c.kube.CoreV1().Pods(applicationNamespace.Name).Delete(ctx, foreign.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	extra := applicationPod.DeepCopy()
	extra.Name, extra.UID = "application-extra", "application-extra-uid"
	if _, err := c.kube.CoreV1().Pods(applicationNamespace.Name).Create(ctx, extra, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckManagedPlatformNodeReservations(ctx, map[string]ManagedPlatformNodeReservation{node.Name: reservation}); err == nil {
		t.Fatal("application pods exceeded their durable reservation without capacity accounting")
	}
	if err := c.CheckDatabaseNodeReservations(ctx, map[string]DatabaseNodeReservation{node.Name: databaseReservation}); err == nil {
		t.Fatal("database-node accounting ignored a durable application envelope overrun")
	}
}

func TestPooledDatabasePodRequiresExactCustomControllerChain(t *testing.T) {
	ctx := context.Background()
	databaseID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(databaseID), UID: "database-namespace", Labels: map[string]string{managedBy: "hakopod", databaseOwner: databaseID, "hakopod.io/project": "project", "hakopod.io/environment": "production"}}}
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "clickhouse.altinity.com/v1",
		"kind":       "ClickHouseInstallation",
		"metadata": map[string]any{
			"name":      "database",
			"namespace": namespace.Name,
			"uid":       "database-controller",
			"labels": map[string]any{managedBy: "hakopod", databaseOwner: databaseID,
				"hakopod.io/project": "project", "hakopod.io/environment": "production"},
		},
	}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "database-0", Namespace: namespace.Name, UID: "database-pod", OwnerReferences: []metav1.OwnerReference{{APIVersion: root.GetAPIVersion(), Kind: root.GetKind(), Name: root.GetName(), UID: root.GetUID(), Controller: ptr(true)}}}}
	c := &Client{kube: fake.NewClientset(namespace), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), root)}
	ownership, err := capacityOwnership(managedplatform.CapacityPoolOwnership{Workloads: []managedplatform.CapacityPoolWorkload{{Kind: "database", ID: databaseID, Project: "project", Environment: "production", Capacity: managedplatform.Capacity{CPUMilli: 500, MemoryBytes: 512 << 20}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, owned, err := c.pooledNamespacePod(ctx, pod, map[string]*corev1.Namespace{}, map[string]bool{}, ownership); err != nil || !owned {
		t.Fatal("exact custom database controller was not recognized", err)
	}
	pod.OwnerReferences[0].UID = "foreign-controller"
	if _, _, owned, err := c.pooledNamespacePod(ctx, pod, map[string]*corev1.Namespace{}, map[string]bool{}, ownership); err != nil || owned {
		t.Fatal("foreign custom database controller was accepted", err)
	}
}

func TestPooledVitessPodRequiresChainToExactRoot(t *testing.T) {
	ctx := context.Background()
	databaseID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(databaseID), UID: "database-namespace", Labels: map[string]string{managedBy: "hakopod", databaseOwner: databaseID, "hakopod.io/project": "project", "hakopod.io/environment": "production"}}}
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "planetscale.com/v2", "kind": "VitessCluster",
		"metadata": map[string]any{"name": "database", "namespace": namespace.Name, "uid": "database-controller", "labels": map[string]any{managedBy: "hakopod", databaseOwner: databaseID, "hakopod.io/project": "project", "hakopod.io/environment": "production"}},
	}}
	shard := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "planetscale.com/v2", "kind": "VitessShard",
		"metadata": map[string]any{"name": "database-zone1-x80", "namespace": namespace.Name, "uid": "database-shard", "ownerReferences": []any{map[string]any{"apiVersion": root.GetAPIVersion(), "kind": root.GetKind(), "name": root.GetName(), "uid": string(root.GetUID()), "controller": true}}},
	}}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "database-zone1-x80-0", Namespace: namespace.Name, UID: "database-pod", Labels: map[string]string{"planetscale.com/cluster": "database"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: shard.GetAPIVersion(), Kind: shard.GetKind(), Name: shard.GetName(), UID: shard.GetUID(), Controller: ptr(true)}}}}
	c := &Client{kube: fake.NewClientset(namespace), dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), root, shard)}
	ownership, err := capacityOwnership(managedplatform.CapacityPoolOwnership{Workloads: []managedplatform.CapacityPoolWorkload{{Kind: "database", ID: databaseID, Project: "project", Environment: "production", Capacity: managedplatform.Capacity{CPUMilli: 500, MemoryBytes: 512 << 20}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, owned, err := c.pooledNamespacePod(ctx, pod, map[string]*corev1.Namespace{}, map[string]bool{}, ownership); err != nil || !owned {
		t.Fatal("exact Vitess controller chain was not recognized", err)
	}
	pod.OwnerReferences[0].UID = "foreign-shard"
	if _, _, owned, err := c.pooledNamespacePod(ctx, pod, map[string]*corev1.Namespace{}, map[string]bool{}, ownership); err != nil || owned {
		t.Fatal("foreign Vitess controller chain was accepted", err)
	}
}

func TestCapacityHeadroomRejectsOverflow(t *testing.T) {
	if _, _, err := capacityWithHeadroom(1<<63-1, 1); err == nil {
		t.Fatal("CPU headroom overflow was accepted")
	}
	if _, _, err := capacityWithHeadroom(1, 1<<63-1); err == nil {
		t.Fatal("memory headroom overflow was accepted")
	}
	huge := corev1.PodSpec{Containers: []corev1.Container{
		{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("9223372036854775807m"), corev1.ResourceMemory: resource.MustParse("9223372036854775807")}}},
		{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1m"), corev1.ResourceMemory: resource.MustParse("1")}}},
	}}
	if _, _, err := podCapacityUsage(huge); err == nil {
		t.Fatal("pod resource request overflow was accepted")
	}
}

func TestCapacityRequestRoundsDecimalQuantitiesWithoutOverflow(t *testing.T) {
	for _, test := range []struct {
		name, cpu, memory   string
		wantCPU, wantMemory int64
		invalid             bool
	}{
		{name: "decimal", cpu: "0.1255", memory: "512.1", wantCPU: 126, wantMemory: 513},
		{name: "binary fraction", cpu: "1.5", memory: "1.5Gi", wantCPU: 1500, wantMemory: 1610612736},
		{name: "maximum", cpu: "9223372036854775.807", memory: "9223372036854775807", wantCPU: math.MaxInt64, wantMemory: math.MaxInt64},
		{name: "CPU rounding overflow", cpu: "9223372036854775.8071", memory: "1", invalid: true},
		{name: "memory rounding overflow", cpu: "1m", memory: "9223372036854775807.1", invalid: true},
		{name: "negative CPU", cpu: "-1m", memory: "1", invalid: true},
		{name: "negative memory", cpu: "1m", memory: "-1", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cpu, memory, err := capacityRequest(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(test.cpu), corev1.ResourceMemory: resource.MustParse(test.memory)})
			if (err != nil) != test.invalid || err == nil && (cpu != test.wantCPU || memory != test.wantMemory) {
				t.Fatalf("capacity = (%d, %d), error = %v", cpu, memory, err)
			}
		})
	}
}

func TestPodCapacityIncludesRestartableInitStagesAndOverhead(t *testing.T) {
	container := func(cpu, memory string, restartable bool) corev1.Container {
		c := corev1.Container{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}}}
		if restartable {
			c.RestartPolicy = ptr(corev1.ContainerRestartPolicyAlways)
		}
		return c
	}
	spec := corev1.PodSpec{
		Containers:     []corev1.Container{container("200m", "200", false)},
		InitContainers: []corev1.Container{container("100m", "100", true), container("500m", "500", false), container("50m", "50", true)},
		Overhead:       corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("25m"), corev1.ResourceMemory: resource.MustParse("25")},
	}
	cpu, memory, err := podCapacityUsage(spec)
	if err != nil || cpu != 625 || memory != 625 {
		t.Fatalf("initialization peak plus overhead = (%d, %d), error = %v", cpu, memory, err)
	}
	if !workloadRequestsMatch(spec, managedplatform.Capacity{CPUMilli: 625, MemoryBytes: 625 + managedplatform.PodMemoryOverheadBytes}) {
		t.Fatal("valid scheduler request envelope was rejected")
	}
	spec.Overhead[corev1.ResourceMemory] = *resource.NewQuantity(math.MaxInt64, resource.DecimalSI)
	if _, _, err := podCapacityUsage(spec); err == nil {
		t.Fatal("Pod overhead overflow was accepted")
	}
	if workloadRequestsMatch(spec, managedplatform.Capacity{CPUMilli: 625, MemoryBytes: 625}) {
		t.Fatal("overflowing requests matched a reserved workload")
	}
	maxMemory := corev1.PodSpec{Containers: []corev1.Container{container("1m", "9223372036854775807", false)}}
	if workloadRequestsMatch(maxMemory, managedplatform.Capacity{CPUMilli: 1, MemoryBytes: math.MaxInt64}) {
		t.Fatal("managed platform memory allowance overflow was accepted")
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
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker", UID: "uid-worker"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{Architecture: "amd64", OperatingSystem: "linux"}, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("2Gi")}}}
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
