//go:build hakopod_native_acceptance && linux

package cluster

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

type NativeNeonCancellationObservation struct {
	SchemaVersion             int            `json:"schema_version"`
	OperationID               string         `json:"operation_id"`
	Project                   string         `json:"project"`
	Environment               string         `json:"environment"`
	SourcePlatformID          string         `json:"source_platform_id"`
	SourceRevision            int64          `json:"source_revision"`
	TargetPlatformID          string         `json:"target_platform_id"`
	TargetRevision            int64          `json:"target_revision"`
	ArtifactID                string         `json:"artifact_id"`
	ManifestSHA256            string         `json:"manifest_sha256"`
	TenantID                  string         `json:"tenant_id"`
	TimelineID                string         `json:"timeline_id"`
	TenantGeneration          int64          `json:"tenant_generation"`
	TimelineGeneration        int64          `json:"timeline_generation"`
	JournalEntries            int            `json:"journal_entries"`
	JournalPhaseCounts        map[string]int `json:"journal_phase_counts"`
	CleanupPending            bool           `json:"cleanup_pending"`
	OperationAuthorityRefused bool           `json:"operation_authority_refused"`
	NamespaceUID              string         `json:"namespace_uid"`
	DeploymentCount           int            `json:"deployment_count"`
	StatefulSetCount          int            `json:"statefulset_count"`
	WorkloadReplicasZero      bool           `json:"workload_replicas_zero"`
	PodsAbsent                bool           `json:"pods_absent"`
	StagingPrefixEmpty        bool           `json:"staging_prefix_empty"`
}

func nativeCancellationWorkload(name string) bool {
	return name == "neon-proxy" || strings.HasPrefix(name, "neon-compute-") || strings.HasPrefix(name, "neon-pageserver-") || strings.HasPrefix(name, "neon-safekeeper-")
}

func nativeCancellationPod(podLabels map[string]string) bool {
	component := podLabels["app.kubernetes.io/component"]
	role := podLabels["hakopod.io/neon-role"]
	return component == "proxy" || strings.HasPrefix(component, "compute-") || strings.HasPrefix(component, "pageserver-") || strings.HasPrefix(component, "safekeeper-") || role == "pageserver" || role == "safekeeper"
}

func nativeCancellationControlWorkload(kind, name string) bool {
	return kind == "deployment" && (name == "neon-broker" || name == "neon-storage-controller") || kind == "statefulset" && name == "neon-controller-database"
}

func nativeCancellationControlPod(component, role string) bool {
	return component == role && (role == "broker" || role == "storage-controller" || role == "controller-database")
}

func nativeCancellationReplicasBounded(spec *int32, values ...int32) bool {
	if spec == nil || *spec != 1 {
		return false
	}
	for _, value := range values {
		if value < 0 || value > 1 {
			return false
		}
	}
	return true
}

func (c *Client) validateNativeNeonCancellationKubernetes(ctx context.Context, receipt store.NativeNeonCancellationReceipt, request NeonRuntimeRequest) (int, int, error) {
	if request.Render.Spec.Neon == nil || request.Render.Spec.Neon.ComputeReplicas < 1 || request.Render.Spec.Neon.ComputeReplicas > 6 || request.Render.Spec.Neon.Pageservers < 2 || request.Render.Spec.Neon.Pageservers > 8 || request.Render.Spec.Neon.Safekeepers != 3 {
		return 0, 0, fmt.Errorf("native Neon cancellation topology is invalid")
	}
	namespace := "managed-platform-" + receipt.TargetPlatformID
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil || ns.DeletionTimestamp != nil || string(ns.UID) != receipt.NamespaceUID || ns.Labels["app.kubernetes.io/managed-by"] != "hakopod" || ns.Labels["hakopod.io/managed-platform-id"] != receipt.TargetPlatformID || ns.Labels["hakopod.io/owner-operation-id"] != request.Operation.ID {
		return 0, 0, fmt.Errorf("native Neon cancellation namespace identity changed")
	}
	wanted := make(map[string]store.NativeCancellationWorkload, len(receipt.Workloads))
	for _, workload := range receipt.Workloads {
		key := workload.Kind + "/" + workload.Name
		if workload.UID == "" || !nativeCancellationWorkload(workload.Name) || workload.Kind != "deployment" && workload.Kind != "statefulset" || wanted[key].UID != "" {
			return 0, 0, fmt.Errorf("native Neon cancellation workload receipt is invalid")
		}
		wanted[key] = workload
	}
	expected := map[string]bool{"deployment/neon-proxy": true}
	for i := 0; i < request.Render.Spec.Neon.ComputeReplicas; i++ {
		expected["statefulset/neon-compute-"+strconv.Itoa(i)] = true
	}
	for i := 0; i < request.Render.Spec.Neon.Pageservers; i++ {
		expected["statefulset/neon-pageserver-"+strconv.Itoa(i)] = true
	}
	for i := 0; i < request.Render.Spec.Neon.Safekeepers; i++ {
		expected["statefulset/neon-safekeeper-"+strconv.Itoa(i)] = true
	}
	if len(expected) == 1 || len(expected) > maxNeonRecoveryWorkloads || len(wanted) != len(expected) {
		return 0, 0, fmt.Errorf("native Neon cancellation workload receipt differs from the accepted specification")
	}
	for key := range expected {
		if wanted[key].UID == "" {
			return 0, 0, fmt.Errorf("native Neon cancellation workload receipt is incomplete")
		}
	}
	selector := labels.Set{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": receipt.TargetPlatformID}.AsSelector().String()
	seen := map[string]bool{}
	controls := map[string]bool{}
	deployments, err := c.kube.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: maxNeonRecoveryWorkloads + 1})
	if err != nil || deployments.Continue != "" || len(deployments.Items) > maxNeonRecoveryWorkloads {
		return 0, 0, fmt.Errorf("native Neon cancellation deployment inventory is unavailable or unbounded")
	}
	deploymentCount := 0
	for i := range deployments.Items {
		item := &deployments.Items[i]
		if nativeCancellationControlWorkload("deployment", item.Name) {
			if verifySupabaseOwned(item, receipt.TargetPlatformID, types.UID(receipt.NamespaceUID)) != nil || item.Labels["hakopod.io/owner-operation-id"] != request.Operation.ID || !nativeCancellationReplicasBounded(item.Spec.Replicas, item.Status.Replicas, item.Status.UpdatedReplicas, item.Status.AvailableReplicas, item.Status.ReadyReplicas) {
				return 0, 0, fmt.Errorf("native Neon cancellation control deployment state changed")
			}
			controls["deployment/"+item.Name] = true
			continue
		}
		if !nativeCancellationWorkload(item.Name) {
			return 0, 0, fmt.Errorf("native Neon cancellation has an unexpected owned deployment")
		}
		want, ok := wanted["deployment/"+item.Name]
		if !ok || string(item.UID) != want.UID || verifySupabaseOwned(item, receipt.TargetPlatformID, types.UID(receipt.NamespaceUID)) != nil || item.Labels["hakopod.io/owner-operation-id"] != request.Operation.ID || item.Spec.Replicas == nil || *item.Spec.Replicas != 0 || item.Status.Replicas != 0 || item.Status.UpdatedReplicas != 0 || item.Status.AvailableReplicas != 0 || item.Status.ReadyReplicas != 0 {
			return 0, 0, fmt.Errorf("native Neon cancellation deployment state changed")
		}
		seen["deployment/"+item.Name] = true
		deploymentCount++
	}
	sets, err := c.kube.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: maxNeonRecoveryWorkloads + 1})
	if err != nil || sets.Continue != "" || len(sets.Items) > maxNeonRecoveryWorkloads {
		return 0, 0, fmt.Errorf("native Neon cancellation StatefulSet inventory is unavailable or unbounded")
	}
	statefulSetCount := 0
	for i := range sets.Items {
		item := &sets.Items[i]
		if nativeCancellationControlWorkload("statefulset", item.Name) {
			if verifySupabaseOwned(item, receipt.TargetPlatformID, types.UID(receipt.NamespaceUID)) != nil || item.Labels["hakopod.io/owner-operation-id"] != request.Operation.ID || !nativeCancellationReplicasBounded(item.Spec.Replicas, item.Status.Replicas, item.Status.UpdatedReplicas, item.Status.ReadyReplicas) {
				return 0, 0, fmt.Errorf("native Neon cancellation control StatefulSet state changed")
			}
			controls["statefulset/"+item.Name] = true
			continue
		}
		if !nativeCancellationWorkload(item.Name) {
			return 0, 0, fmt.Errorf("native Neon cancellation has an unexpected owned StatefulSet")
		}
		want, ok := wanted["statefulset/"+item.Name]
		if !ok || string(item.UID) != want.UID || verifySupabaseOwned(item, receipt.TargetPlatformID, types.UID(receipt.NamespaceUID)) != nil || item.Labels["hakopod.io/owner-operation-id"] != request.Operation.ID || item.Spec.Replicas == nil || *item.Spec.Replicas != 0 || item.Status.Replicas != 0 || item.Status.UpdatedReplicas != 0 || item.Status.ReadyReplicas != 0 {
			return 0, 0, fmt.Errorf("native Neon cancellation StatefulSet state changed")
		}
		seen["statefulset/"+item.Name] = true
		statefulSetCount++
	}
	if len(seen) != len(wanted) {
		return 0, 0, fmt.Errorf("native Neon cancellation workload inventory changed")
	}
	for _, key := range []string{"deployment/neon-broker", "deployment/neon-storage-controller", "statefulset/neon-controller-database"} {
		if !controls[key] {
			return 0, 0, fmt.Errorf("native Neon cancellation control workload inventory changed")
		}
	}
	pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: maxNeonRecoveryWorkloads + 1})
	if err != nil || pods.Continue != "" || len(pods.Items) > maxNeonRecoveryWorkloads {
		return 0, 0, fmt.Errorf("native Neon cancellation pod inventory is unavailable or unbounded")
	}
	for _, pod := range pods.Items {
		if nativeCancellationPod(pod.Labels) {
			return 0, 0, fmt.Errorf("native Neon cancellation still has a serving or storage pod")
		}
		component := pod.Labels["app.kubernetes.io/component"]
		role := pod.Labels["hakopod.io/neon-role"]
		if !nativeCancellationControlPod(component, role) || pod.UID == "" || pod.DeletionTimestamp != nil || pod.Labels["hakopod.io/owner-operation-id"] != request.Operation.ID || len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].UID == "" || pod.OwnerReferences[0].Controller == nil || !*pod.OwnerReferences[0].Controller {
			return 0, 0, fmt.Errorf("native Neon cancellation has an unexpected owned pod")
		}
		owner := pod.OwnerReferences[0]
		if component == "controller-database" {
			if owner.APIVersion != "apps/v1" || owner.Kind != "StatefulSet" || owner.Name != "neon-controller-database" || owner.UID != types.UID(wantedControlUID(sets.Items, "neon-controller-database")) {
				return 0, 0, fmt.Errorf("native Neon cancellation control pod ownership changed")
			}
		} else if owner.APIVersion != "apps/v1" || owner.Kind != "ReplicaSet" {
			return 0, 0, fmt.Errorf("native Neon cancellation control pod ownership changed")
		}
		key := component + "/pod"
		if controls[key] {
			return 0, 0, fmt.Errorf("native Neon cancellation control pod inventory changed")
		}
		controls[key] = true
	}
	return deploymentCount, statefulSetCount, nil
}

func wantedControlUID(items []appsv1.StatefulSet, name string) string {
	for i := range items {
		if items[i].Name == name {
			return string(items[i].UID)
		}
	}
	return ""
}

// ObserveNativeNeonCancellation constructs the object client only from the
// authenticated accepted target snapshot, then performs the read-only check.
func (c *Client) ObserveNativeNeonCancellation(ctx context.Context, receipt store.NativeNeonCancellationReceipt, target store.ManagedPlatform, request NeonRuntimeRequest, encryptionKey []byte) (NativeNeonCancellationObservation, error) {
	if len(encryptionKey) != 32 {
		return NativeNeonCancellationObservation{}, fmt.Errorf("native Neon cancellation encryption key is unavailable")
	}
	runtime := &NeonRecoveryRuntime{Cluster: c, EncryptionKey: append([]byte(nil), encryptionKey...)}
	objects, err := runtime.objectStore(target, request)
	if err != nil {
		return NativeNeonCancellationObservation{}, err
	}
	defer objects.Close()
	return c.observeNativeNeonCancellation(ctx, receipt, target, request, objects)
}

// observeNativeNeonCancellation proves the retained target is isolated after
// an interrupted restore. It performs only Kubernetes GET/LIST and one bounded
// object-store ListPrefix call. Keeping the object client injected makes the
// read-only boundary independently testable.
func (c *Client) observeNativeNeonCancellation(ctx context.Context, receipt store.NativeNeonCancellationReceipt, target store.ManagedPlatform, request NeonRuntimeRequest, objects backup.ObjectStore) (NativeNeonCancellationObservation, error) {
	var observation NativeNeonCancellationObservation
	if c == nil || c.kube == nil || objects == nil || receipt.SchemaVersion != 1 || receipt.OperationID == "" || receipt.TargetPlatformID == "" || receipt.TargetRevision < 1 || receipt.NamespaceUID == "" || receipt.StagingPrefix == "" || !receipt.OperationAuthorityRefused || receipt.CleanupPending || receipt.JournalEntries < 1 || target.ID != receipt.TargetPlatformID || target.Revision != receipt.TargetRevision || target.Project != receipt.Project || target.Environment != receipt.Environment || target.Status != "failed" || target.DeletedAt != nil || target.Observation["phase"] != "recovery-isolated" || target.Observation["recovery_operation_id"] != receipt.OperationID || request.Operation.ID == "" || request.Operation.PlatformID != receipt.TargetPlatformID || request.Operation.Revision != receipt.TargetRevision || request.Render.PlatformID != receipt.TargetPlatformID || request.Render.Revision != receipt.TargetRevision || request.Render.Spec.Kind != "neon" || !reflect.DeepEqual(request.Render.Spec, target.Spec) {
		return observation, fmt.Errorf("native Neon cancellation observation requires the exact accepted target snapshot")
	}
	deployments, statefulSets, err := c.validateNativeNeonCancellationKubernetes(ctx, receipt, request)
	if err != nil {
		return observation, err
	}
	keys, next, err := objects.ListPrefix(ctx, receipt.StagingPrefix, "")
	if err != nil || len(keys) != 0 || next != "" {
		return observation, fmt.Errorf("native Neon cancellation staging prefix is not empty")
	}
	deploymentsAfter, statefulSetsAfter, err := c.validateNativeNeonCancellationKubernetes(ctx, receipt, request)
	if err != nil || deploymentsAfter != deployments || statefulSetsAfter != statefulSets {
		return observation, fmt.Errorf("native Neon cancellation Kubernetes state changed during observation")
	}
	observation = NativeNeonCancellationObservation{SchemaVersion: 1, OperationID: receipt.OperationID, Project: receipt.Project,
		Environment: receipt.Environment, SourcePlatformID: receipt.SourcePlatformID, SourceRevision: receipt.SourceRevision,
		TargetPlatformID: receipt.TargetPlatformID, TargetRevision: receipt.TargetRevision, ArtifactID: receipt.ArtifactID,
		ManifestSHA256: receipt.ManifestSHA256, TenantID: receipt.TenantID, TimelineID: receipt.TimelineID,
		TenantGeneration: receipt.TenantGeneration, TimelineGeneration: receipt.TimelineGeneration,
		JournalEntries: receipt.JournalEntries, JournalPhaseCounts: receipt.JournalPhaseCounts, CleanupPending: false,
		OperationAuthorityRefused: true, NamespaceUID: receipt.NamespaceUID, DeploymentCount: deployments,
		StatefulSetCount: statefulSets, WorkloadReplicasZero: true, PodsAbsent: true, StagingPrefixEmpty: true}
	return observation, nil
}
