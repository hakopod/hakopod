//go:build hakopod_native_acceptance && linux

package cluster

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

type nativeCancellationObjects struct {
	listed int
	keys   []string
	next   string
}

func (*nativeCancellationObjects) Put(context.Context, string, io.Reader, int64) (int64, string, error) {
	return 0, "", fmt.Errorf("write forbidden")
}
func (*nativeCancellationObjects) Get(context.Context, string) (io.ReadCloser, int64, error) {
	return nil, 0, fmt.Errorf("read outside prefix forbidden")
}
func (*nativeCancellationObjects) Delete(context.Context, string) error {
	return fmt.Errorf("delete forbidden")
}
func (o *nativeCancellationObjects) ListPrefix(context.Context, string, string) ([]string, string, error) {
	o.listed++
	return o.keys, o.next, nil
}
func (*nativeCancellationObjects) DeletePrefix(context.Context, string) (bool, error) {
	return true, fmt.Errorf("delete forbidden")
}
func (*nativeCancellationObjects) Close() {}

func nativeCancellationFixture(t *testing.T) (*Client, store.NativeNeonCancellationReceipt, store.ManagedPlatform, NeonRuntimeRequest, *nativeCancellationObjects) {
	t.Helper()
	platformID := "11111111111111111111111111111111"
	namespace, namespaceUID, ownerID := "managed-platform-"+platformID, types.UID("namespace-uid"), "accepted-operation"
	zero := int32(0)
	one := int32(1)
	controller := true
	owner := []metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: namespace, UID: namespaceUID}}
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(name + "-uid"), Labels: map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": platformID, "hakopod.io/owner-operation-id": ownerID}, OwnerReferences: owner}
	}
	objects := []runtime.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: namespaceUID, Labels: map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": platformID, "hakopod.io/owner-operation-id": ownerID}}}}
	for _, name := range []string{"neon-broker", "neon-storage-controller"} {
		objects = append(objects, &appsv1.Deployment{ObjectMeta: meta(name), Spec: appsv1.DeploymentSpec{Replicas: &one}, Status: appsv1.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1, ReadyReplicas: 1}})
		component := name[len("neon-"):]
		podMeta := meta(name + "-pod")
		podMeta.Labels["app.kubernetes.io/component"], podMeta.Labels["hakopod.io/neon-role"] = component, component
		podMeta.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: name + "-fixture", UID: types.UID(name + "-replicaset-uid"), Controller: &controller}}
		objects = append(objects, &corev1.Pod{ObjectMeta: podMeta})
	}
	controllerDatabase := &appsv1.StatefulSet{ObjectMeta: meta("neon-controller-database"), Spec: appsv1.StatefulSetSpec{Replicas: &one}, Status: appsv1.StatefulSetStatus{Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1}}
	objects = append(objects, controllerDatabase)
	controlPodMeta := meta("neon-controller-database-0")
	controlPodMeta.Labels["app.kubernetes.io/component"], controlPodMeta.Labels["hakopod.io/neon-role"] = "controller-database", "controller-database"
	controlPodMeta.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: controllerDatabase.Name, UID: controllerDatabase.UID, Controller: &controller}}
	objects = append(objects, &corev1.Pod{ObjectMeta: controlPodMeta})
	workloads := []store.NativeCancellationWorkload{{Kind: "deployment", Name: "neon-proxy", UID: "neon-proxy-uid"}}
	objects = append(objects, &appsv1.Deployment{ObjectMeta: meta("neon-proxy"), Spec: appsv1.DeploymentSpec{Replicas: &zero}})
	for _, name := range []string{"neon-compute-0", "neon-pageserver-0", "neon-pageserver-1", "neon-safekeeper-0", "neon-safekeeper-1", "neon-safekeeper-2"} {
		objects = append(objects, &appsv1.StatefulSet{ObjectMeta: meta(name), Spec: appsv1.StatefulSetSpec{Replicas: &zero}})
		workloads = append(workloads, store.NativeCancellationWorkload{Kind: "statefulset", Name: name, UID: name + "-uid"})
	}
	c := &Client{kube: fake.NewSimpleClientset(objects...)}
	spec := managedplatform.Spec{Kind: "neon", Neon: &managedplatform.NeonConfig{ComputeReplicas: 1, Pageservers: 2, Safekeepers: 3}}
	target := store.ManagedPlatform{ID: platformID, Project: "demo", Environment: "development", Revision: 1, Spec: spec, Status: "failed", Observation: map[string]any{"phase": "recovery-isolated", "recovery_operation_id": "cancelled-operation"}}
	request := NeonRuntimeRequest{Operation: store.ManagedPlatformOperation{ID: ownerID, PlatformID: platformID, Revision: 1}, Render: managedplatform.NeonRenderInput{PlatformID: platformID, Revision: 1, Spec: spec}}
	receipt := store.NativeNeonCancellationReceipt{SchemaVersion: 1, OperationID: "cancelled-operation", Project: target.Project, Environment: target.Environment, SourcePlatformID: "source", SourceRevision: 1, TargetPlatformID: platformID, TargetRevision: 1, ArtifactID: "artifact", ManifestSHA256: "digest", JournalEntries: 1, JournalPhaseCounts: map[string]int{"complete": 1}, OperationAuthorityRefused: true, NamespaceUID: string(namespaceUID), Workloads: workloads, StagingPrefix: "fixture/recovery/cancelled-operation/"}
	remote := &nativeCancellationObjects{}
	return c, receipt, target, request, remote
}

func TestObserveNativeNeonCancellationReadsExactZeroInventory(t *testing.T) {
	c, receipt, target, request, remote := nativeCancellationFixture(t)
	result, err := c.observeNativeNeonCancellation(context.Background(), receipt, target, request, remote)
	if err != nil {
		t.Fatal(err)
	}
	if !result.WorkloadReplicasZero || !result.PodsAbsent || !result.StagingPrefixEmpty || result.DeploymentCount != 1 || result.StatefulSetCount != 6 || remote.listed != 1 {
		t.Fatalf("unexpected cancellation observation: %#v", result)
	}
}

func TestValidateNativeNeonCancellationRejectsInvalidTopology(t *testing.T) {
	for _, test := range []struct {
		name string
		neon *managedplatform.NeonConfig
	}{
		{name: "missing", neon: nil},
		{name: "too few pageservers", neon: &managedplatform.NeonConfig{ComputeReplicas: 1, Pageservers: 1, Safekeepers: 3}},
		{name: "wrong safekeeper count", neon: &managedplatform.NeonConfig{ComputeReplicas: 1, Pageservers: 2, Safekeepers: 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, receipt, _, request, _ := nativeCancellationFixture(t)
			request.Render.Spec.Neon = test.neon
			if _, _, err := c.validateNativeNeonCancellationKubernetes(context.Background(), receipt, request); err == nil {
				t.Fatal("invalid Neon topology qualified")
			}
			if len(c.kube.(*fake.Clientset).Actions()) != 0 {
				t.Fatal("invalid topology reached Kubernetes")
			}
		})
	}
}

func TestObserveNativeNeonCancellationRefusesChangedInventory(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, *Client, *store.NativeNeonCancellationReceipt, *nativeCancellationObjects)
	}{
		{name: "nonzero replicas", change: func(t *testing.T, c *Client, _ *store.NativeNeonCancellationReceipt, _ *nativeCancellationObjects) {
			item, err := c.kube.AppsV1().StatefulSets("managed-platform-11111111111111111111111111111111").Get(context.Background(), "neon-compute-0", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			one := int32(1)
			item.Spec.Replicas = &one
			if _, err = c.kube.AppsV1().StatefulSets(item.Namespace).Update(context.Background(), item, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing workload", change: func(t *testing.T, c *Client, _ *store.NativeNeonCancellationReceipt, _ *nativeCancellationObjects) {
			if err := c.kube.AppsV1().StatefulSets("managed-platform-11111111111111111111111111111111").Delete(context.Background(), "neon-pageserver-0", metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing retained control workload", change: func(t *testing.T, c *Client, receipt *store.NativeNeonCancellationReceipt, _ *nativeCancellationObjects) {
			if err := c.kube.AppsV1().Deployments("managed-platform-"+receipt.TargetPlatformID).Delete(context.Background(), "neon-broker", metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unbounded retained control status", change: func(t *testing.T, c *Client, receipt *store.NativeNeonCancellationReceipt, _ *nativeCancellationObjects) {
			namespace := "managed-platform-" + receipt.TargetPlatformID
			item, err := c.kube.AppsV1().Deployments(namespace).Get(context.Background(), "neon-storage-controller", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			item.Status.ReadyReplicas = 2
			if _, err = c.kube.AppsV1().Deployments(namespace).UpdateStatus(context.Background(), item, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "extra relevant workload", change: func(_ *testing.T, _ *Client, receipt *store.NativeNeonCancellationReceipt, _ *nativeCancellationObjects) {
			receipt.Workloads = append(receipt.Workloads, store.NativeCancellationWorkload{Kind: "statefulset", Name: "neon-compute-1", UID: "extra-uid"})
		}},
		{name: "unknown owned deployment", change: func(t *testing.T, c *Client, receipt *store.NativeNeonCancellationReceipt, _ *nativeCancellationObjects) {
			one := int32(1)
			namespace := "managed-platform-" + receipt.TargetPlatformID
			_, err := c.kube.AppsV1().Deployments(namespace).Create(context.Background(), &appsv1.Deployment{ObjectMeta: nativeCancellationOwnedMeta(namespace, receipt.TargetPlatformID, "accepted-operation", "neon-unknown-deployment"), Spec: appsv1.DeploymentSpec{Replicas: &one}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unknown owned StatefulSet", change: func(t *testing.T, c *Client, receipt *store.NativeNeonCancellationReceipt, _ *nativeCancellationObjects) {
			one := int32(1)
			namespace := "managed-platform-" + receipt.TargetPlatformID
			_, err := c.kube.AppsV1().StatefulSets(namespace).Create(context.Background(), &appsv1.StatefulSet{ObjectMeta: nativeCancellationOwnedMeta(namespace, receipt.TargetPlatformID, "accepted-operation", "neon-unknown-statefulset"), Spec: appsv1.StatefulSetSpec{Replicas: &one}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unknown owned pod", change: func(t *testing.T, c *Client, receipt *store.NativeNeonCancellationReceipt, _ *nativeCancellationObjects) {
			namespace := "managed-platform-" + receipt.TargetPlatformID
			metadata := nativeCancellationOwnedMeta(namespace, receipt.TargetPlatformID, "accepted-operation", "neon-unknown-pod")
			metadata.Labels["app.kubernetes.io/component"], metadata.Labels["hakopod.io/neon-role"] = "mystery", "mystery"
			_, err := c.kube.CoreV1().Pods(namespace).Create(context.Background(), &corev1.Pod{ObjectMeta: metadata}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "component pod present", change: func(t *testing.T, c *Client, receipt *store.NativeNeonCancellationReceipt, _ *nativeCancellationObjects) {
			namespace := "managed-platform-" + receipt.TargetPlatformID
			_, err := c.kube.CoreV1().Pods(namespace).Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "compute-pod", Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": receipt.TargetPlatformID, "app.kubernetes.io/component": "compute-0"}}}, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "object keys", change: func(_ *testing.T, _ *Client, _ *store.NativeNeonCancellationReceipt, remote *nativeCancellationObjects) {
			remote.keys = []string{"staging/object"}
		}},
		{name: "object continuation", change: func(_ *testing.T, _ *Client, _ *store.NativeNeonCancellationReceipt, remote *nativeCancellationObjects) {
			remote.next = "more"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, receipt, target, request, remote := nativeCancellationFixture(t)
			test.change(t, c, &receipt, remote)
			client := c.kube.(*fake.Clientset)
			before := len(client.Actions())
			if _, err := c.observeNativeNeonCancellation(context.Background(), receipt, target, request, remote); err == nil {
				t.Fatal("changed cancellation state qualified")
			}
			for _, action := range client.Actions()[before:] {
				if action.GetVerb() != "get" && action.GetVerb() != "list" {
					t.Fatalf("observation attempted Kubernetes mutation %s %s", action.GetVerb(), action.GetResource().Resource)
				}
			}
		})
	}
}

func nativeCancellationOwnedMeta(namespace, platformID, ownerID, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(name + "-uid"), Labels: map[string]string{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": platformID, "hakopod.io/owner-operation-id": ownerID}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: namespace, UID: types.UID("namespace-uid")}}}
}

func TestObserveNativeNeonCancellationRefusesNamespaceDriftAfterObjectCheck(t *testing.T) {
	c, receipt, target, request, remote := nativeCancellationFixture(t)
	gets := 0
	client := c.kube.(*fake.Clientset)
	client.PrependReactor("get", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets < 2 {
			return false, nil, nil
		}
		namespace := "managed-platform-" + receipt.TargetPlatformID
		return true, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: types.UID("changed-uid")}}, nil
	})
	if _, err := c.observeNativeNeonCancellation(context.Background(), receipt, target, request, remote); err == nil {
		t.Fatal("namespace drift during observation qualified")
	}
	if remote.listed != 1 {
		t.Fatalf("object prefix was checked %d times", remote.listed)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" && action.GetVerb() != "list" {
			t.Fatalf("observation attempted Kubernetes mutation %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
}
