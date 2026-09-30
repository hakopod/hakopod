package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const probeTestInstallation = "0123456789abcdef0123456789abcdef"
const probeTestNodeUID types.UID = "00000000-0000-0000-0000-000000000001"
const probeTestRuntimeUID types.UID = "00000000-0000-0000-0000-000000000002"
const probeTestNamespaceUID types.UID = "00000000-0000-0000-0000-000000000003"
const probeTestPodUID types.UID = "00000000-0000-0000-0000-000000000004"

type workspaceProbeTestHost struct {
	machine       string
	allocated     uint64
	removed       bool
	removalChecks int
	observeErr    error
	rootInode     uint64
}

func (h *workspaceProbeTestHost) MachineID() (string, error)            { return h.machine, nil }
func (h *workspaceProbeTestHost) RootIdentity() (uint64, uint64, error) { return 4, h.rootInode, nil }
func (h *workspaceProbeTestHost) Observe(uid types.UID) (actionsWorkspaceDiskSample, error) {
	if uid != probeTestPodUID {
		return actionsWorkspaceDiskSample{}, fmt.Errorf("wrong pod UID")
	}
	return actionsWorkspaceDiskSample{Name: ".gvisor.filestore." + strings.Repeat("a", 64), Device: 4, Inode: 7, LogicalBytes: 1 << 30, AllocatedBytes: h.allocated}, h.observeErr
}
func (h *workspaceProbeTestHost) Removed(uid types.UID) (bool, error) {
	if uid != probeTestPodUID {
		return false, fmt.Errorf("wrong cleanup UID")
	}
	h.removalChecks++
	return h.removed, nil
}

func newWorkspaceProbeTest(t *testing.T) (*actionsWorkspaceProbe, *fake.Clientset, *workspaceProbeTestHost) {
	t.Helper()
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	host := &workspaceProbeTestHost{machine: probeTestInstallation, allocated: 32 << 20, removed: true, rootInode: 10}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node", UID: probeTestNodeUID, Labels: map[string]string{actionsWorkspaceInstallationLabel: probeTestInstallation, "kubernetes.io/arch": runtime.GOARCH, "hakopod.io/actions-runtime": "ready", ActionsWorkspaceCapabilityLabel: string(ActionsWorkspaceSharedOverlay2V1)}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{MachineID: host.machine, BootID: "boot-1", Architecture: runtime.GOARCH}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	rc := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: ActionsRuntime, UID: probeTestRuntimeUID, Labels: map[string]string{managedBy: "hakopod", actionsWorkspaceInstallationLabel: probeTestInstallation}}, Handler: ActionsRuntime, Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"hakopod.io/actions-runtime": "ready"}}, Overhead: &nodev1.Overhead{PodFixed: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("512Mi")}}}
	kube := fake.NewSimpleClientset(node, rc)
	kube.PrependReactor("create", "namespaces", func(action ktesting.Action) (bool, kruntime.Object, error) {
		action.(ktesting.CreateAction).GetObject().(*corev1.Namespace).UID = probeTestNamespaceUID
		return false, nil, nil
	})
	kube.PrependReactor("create", "pods", func(action ktesting.Action) (bool, kruntime.Object, error) {
		pod := action.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		pod.UID, pod.Spec.NodeName, pod.Status.Phase = probeTestPodUID, node.Name, corev1.PodRunning
		return false, nil, nil
	})
	p := &actionsWorkspaceProbe{kube: kube, host: host, options: ActionsWorkspaceProbeOptions{Kubeconfig: "/operator/kubeconfig", Context: "development", NodeName: node.Name, InstallationID: probeTestInstallation, Profile: ActionsWorkspaceSharedOverlay2V1}, now: func() time.Time { return now }, wait: func(context.Context, time.Duration) error { return context.DeadlineExceeded }}
	p.execute = func(_ context.Context, pod *corev1.Pod, container string, command []string) ([]byte, error) {
		if pod.UID != probeTestPodUID {
			return nil, fmt.Errorf("wrong execution UID")
		}
		if container == "docker" || strings.Contains(command[len(command)-1], "proof.mkdir") {
			return []byte("42\n"), nil
		}
		if strings.Contains(command[len(command)-1], "os.urandom") {
			host.allocated += actionsWorkspaceProbeBytes
		}
		if strings.Contains(command[len(command)-1], "os.unlink") {
			host.allocated -= actionsWorkspaceProbeBytes
		}
		return nil, nil
	}
	p.usage = func(context.Context) (uint64, bool, error) { return host.allocated, true, nil }
	return p, kube, host
}

func TestActionsWorkspaceProbePublishesOnlyAfterOwnedCleanup(t *testing.T) {
	p, kube, host := newWorkspaceProbeTest(t)
	var deleted []string
	kube.PrependReactor("delete", "*", func(action ktesting.Action) (bool, kruntime.Object, error) {
		deletion := action.(ktesting.DeleteAction)
		preconditions := deletion.GetDeleteOptions().Preconditions
		if preconditions == nil || preconditions.UID == nil {
			t.Fatal("cleanup omitted UID precondition")
		}
		switch action.GetResource().Resource {
		case "pods":
			if *preconditions.UID != probeTestPodUID {
				t.Fatal("cleanup selected wrong pod")
			}
			deleted = append(deleted, "pod")
		case "namespaces":
			if *preconditions.UID != probeTestNamespaceUID || host.removalChecks == 0 {
				t.Fatal("namespace deleted before physical workspace removal")
			}
			deleted = append(deleted, "namespace")
		}
		return false, nil, nil
	})
	kube.PrependReactor("update", "nodes", func(action ktesting.Action) (bool, kruntime.Object, error) {
		node := action.(ktesting.UpdateAction).GetObject().(*corev1.Node)
		if node.Labels[ActionsWorkspaceCapabilityLabel] != "" && (strings.Join(deleted, ",") != "pod,namespace" || node.Annotations[ActionsWorkspaceProbeLockAnnotation] != "") {
			t.Fatal("capability published before complete cleanup")
		}
		return false, nil, nil
	})
	if err := p.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	node, _ := kube.CoreV1().Nodes().Get(context.Background(), p.options.NodeName, metav1.GetOptions{})
	if node.Labels[ActionsWorkspaceCapabilityLabel] != string(ActionsWorkspaceSharedOverlay2V1) || node.Annotations[ActionsWorkspaceProbeLockAnnotation] != "" {
		t.Fatal("successful probe did not publish and release atomically")
	}
	if _, err := kube.CoreV1().Namespaces().Get(context.Background(), p.lock.Namespace, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("probe namespace remains")
	}
}

func TestActionsWorkspaceProbeFailureCleansWithoutQualification(t *testing.T) {
	p, kube, host := newWorkspaceProbeTest(t)
	host.observeErr = errors.New("private host detail must not be returned")
	err := p.run(context.Background())
	if err == nil || strings.Contains(err.Error(), "private host detail") {
		t.Fatal("physical proof failure was lost or leaked")
	}
	node, _ := kube.CoreV1().Nodes().Get(context.Background(), p.options.NodeName, metav1.GetOptions{})
	if node.Labels[ActionsWorkspaceCapabilityLabel] != "" || node.Annotations[ActionsWorkspaceProbeLockAnnotation] != "" || host.removalChecks == 0 {
		t.Fatal("failed probe left eligibility or skipped cleanup")
	}
}

func TestActionsWorkspaceProbeIncompleteCleanupRetainsLock(t *testing.T) {
	p, kube, host := newWorkspaceProbeTest(t)
	host.removed = false
	if err := p.run(context.Background()); err == nil || !strings.Contains(err.Error(), "cleanup is incomplete") {
		t.Fatal("missing physical deletion must fail closed", err)
	}
	node, _ := kube.CoreV1().Nodes().Get(context.Background(), p.options.NodeName, metav1.GetOptions{})
	if node.Labels[ActionsWorkspaceCapabilityLabel] != "" || node.Annotations[ActionsWorkspaceProbeLockAnnotation] == "" {
		t.Fatal("incomplete cleanup released lock or qualified node")
	}
	if _, err := kube.CoreV1().Namespaces().Get(context.Background(), p.lock.Namespace, metav1.GetOptions{}); err != nil {
		t.Fatal("namespace removed before physical cleanup")
	}
}

func TestActionsWorkspaceProbeRefusesConcurrentAndMalformedLocks(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			p, kube, _ := newWorkspaceProbeTest(t)
			node, _ := kube.CoreV1().Nodes().Get(context.Background(), p.options.NodeName, metav1.GetOptions{})
			lock := actionsWorkspaceProbeLock{Version: 1, Nonce: probeTestInstallation, NodeUID: node.UID, RuntimeUID: probeTestRuntimeUID, InstallationID: probeTestInstallation, Namespace: "hakopod-workspace-probe-" + probeTestInstallation, KubeletDevice: 4, KubeletInode: 10, Deadline: p.now().Add(time.Minute)}
			raw, _ := json.Marshal(lock)
			if malformed {
				raw = []byte(`{"version":99}`)
			}
			node.Annotations = map[string]string{ActionsWorkspaceProbeLockAnnotation: string(raw)}
			kube.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
			kube.ClearActions()
			if err := p.run(context.Background()); err == nil {
				t.Fatal("existing lock accepted")
			}
			for _, action := range kube.Actions() {
				if action.GetVerb() != "get" {
					t.Fatalf("existing lock caused mutation: %s", action.GetVerb())
				}
			}
		})
	}
}

func TestActionsWorkspaceProbeRecoversExpiredOwnedCleanupWithoutQualifying(t *testing.T) {
	p, kube, _ := newWorkspaceProbeTest(t)
	if err := p.verifyRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ok, err := p.acquire(context.Background()); err != nil || !ok {
		t.Fatal(err)
	}
	if err := p.create(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := p.lockText()
	p.lock.Deadline = p.now().Add(-time.Hour)
	if err := p.changeLock(context.Background(), old, &p.lock, false); err != nil {
		t.Fatal(err)
	}
	if err := p.run(context.Background()); err == nil || !strings.Contains(err.Error(), "cleanup completed") {
		t.Fatal("expired owned probe was not cleaned", err)
	}
	node, _ := kube.CoreV1().Nodes().Get(context.Background(), p.options.NodeName, metav1.GetOptions{})
	if node.Labels[ActionsWorkspaceCapabilityLabel] != "" || node.Annotations[ActionsWorkspaceProbeLockAnnotation] != "" {
		t.Fatal("cleanup recovery must leave qualification disabled")
	}
}

func TestActionsWorkspaceProbeRefusesReplacementPodCleanup(t *testing.T) {
	p, kube, _ := newWorkspaceProbeTest(t)
	if err := p.verifyRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ok, err := p.acquire(context.Background()); err != nil || !ok {
		t.Fatal(err)
	}
	if err := p.create(context.Background()); err != nil {
		t.Fatal(err)
	}
	pod, _ := kube.CoreV1().Pods(p.lock.Namespace).Get(context.Background(), actionsWorkspaceProbePodName, metav1.GetOptions{})
	pod.UID = "00000000-0000-0000-0000-000000000099"
	kube.CoreV1().Pods(p.lock.Namespace).Update(context.Background(), pod, metav1.UpdateOptions{})
	kube.ClearActions()
	if err := p.cleanup(context.Background()); err == nil {
		t.Fatal("replacement pod accepted for deletion")
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatal("replacement caused resource deletion")
		}
	}
}

func TestActionsWorkspaceProbeRejectsReplacedKubeletRoot(t *testing.T) {
	p, kube, host := newWorkspaceProbeTest(t)
	if err := p.verifyRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ok, err := p.acquire(context.Background()); err != nil || !ok {
		t.Fatal(err)
	}
	if err := p.create(context.Background()); err != nil {
		t.Fatal(err)
	}
	host.rootInode++
	kube.ClearActions()
	if err := p.cleanup(context.Background()); err == nil {
		t.Fatal("replacement kubelet root accepted as cleanup evidence")
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatal("changed host filesystem caused resource deletion")
		}
	}
	if err := p.changeLock(context.Background(), p.lockText(), nil, true); err == nil {
		t.Fatal("replacement kubelet root accepted for qualification")
	}
}

func TestActionsWorkspaceProbeWithdrawsAfterCancellationAndStillCleans(t *testing.T) {
	p, kube, host := newWorkspaceProbeTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	original := p.execute
	p.execute = func(ctx context.Context, pod *corev1.Pod, container string, command []string) ([]byte, error) {
		if strings.Contains(command[len(command)-1], "proof.mkdir") {
			cancel()
			return nil, context.Canceled
		}
		return original(ctx, pod, container, command)
	}
	if err := p.run(ctx); err == nil {
		t.Fatal("cancelled probe succeeded")
	}
	node, _ := kube.CoreV1().Nodes().Get(context.Background(), p.options.NodeName, metav1.GetOptions{})
	if node.Labels[ActionsWorkspaceCapabilityLabel] != "" || node.Annotations[ActionsWorkspaceProbeLockAnnotation] != "" || host.removalChecks == 0 {
		t.Fatal("cancellation skipped independent cleanup or retained eligibility")
	}
}

func TestActionsWorkspaceProbeWithdrawsStaleCapabilityOnRuntimeFailure(t *testing.T) {
	p, kube, _ := newWorkspaceProbeTest(t)
	rc, _ := kube.NodeV1().RuntimeClasses().Get(context.Background(), ActionsRuntime, metav1.GetOptions{})
	rc.Handler = "runc"
	kube.NodeV1().RuntimeClasses().Update(context.Background(), rc, metav1.UpdateOptions{})
	if err := p.run(context.Background()); err == nil {
		t.Fatal("wrong runtime accepted")
	}
	node, _ := kube.CoreV1().Nodes().Get(context.Background(), p.options.NodeName, metav1.GetOptions{})
	if node.Labels[ActionsWorkspaceCapabilityLabel] != "" {
		t.Fatal("failed preflight left stale qualification")
	}
}

func TestActionsWorkspaceProbeComparesNodeRuntimeValues(t *testing.T) {
	p, kube, _ := newWorkspaceProbeTest(t)
	node, _ := kube.CoreV1().Nodes().Get(context.Background(), p.options.NodeName, metav1.GetOptions{})
	node.Status.NodeInfo.Swap = &corev1.NodeSwapStatus{Capacity: ptr(int64(0))}
	kube.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
	if err := p.verifyRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.verifyRuntime(context.Background()); err != nil {
		t.Fatal("equivalent decoded node runtime was treated as changed", err)
	}
	node.Status.NodeInfo.BootID = "boot-2"
	kube.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
	if err := p.verifyRuntime(context.Background()); err == nil {
		t.Fatal("node reboot was not detected")
	}
}

func TestActionsWorkspaceProbeNodeConflictDoesNotOverwriteAnotherLock(t *testing.T) {
	p, kube, _ := newWorkspaceProbeTest(t)
	if err := p.verifyRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	updates := 0
	kube.PrependReactor("update", "nodes", func(action ktesting.Action) (bool, kruntime.Object, error) {
		updates++
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, p.options.NodeName, errors.New("conflict"))
	})
	if _, err := p.acquire(context.Background()); err == nil || updates != 3 {
		t.Fatal("node conflict retries were not bounded", err, updates)
	}
}

func TestActionsWorkspaceProbePodContainsNoRegistrationOrClusterCredentials(t *testing.T) {
	p, _, _ := newWorkspaceProbeTest(t)
	p.lock = actionsWorkspaceProbeLock{Nonce: probeTestInstallation, Namespace: "test-probe"}
	pod, err := p.desiredPod()
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken || len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].EmptyDir == nil {
		t.Fatal("probe contains credential or host volumes")
	}
	for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		for _, mount := range container.VolumeMounts {
			if mount.Name != "runner" {
				t.Fatal("probe retained a credential mount")
			}
		}
		if strings.Contains(strings.Join(container.Command, " "), "/usr/local/lib/hakopod/observe.py") {
			t.Fatal("probe starts provider registration")
		}
	}
	mutated := pod.DeepCopy()
	mutated.Spec.Containers[0].SecurityContext.Privileged = ptr(true)
	if actionsWorkspaceProbeShape(mutated, pod) {
		t.Fatal("admission security mutation was not detected")
	}
}

func TestActionsWorkspaceProbeToleratesOnlyItsExactOwnedPool(t *testing.T) {
	p, kube, _ := newWorkspaceProbeTest(t)
	node, _ := kube.CoreV1().Nodes().Get(context.Background(), p.options.NodeName, metav1.GetOptions{})
	node.Labels["hakopod.com/pool"] = "actions"
	node.Spec.Taints = []corev1.Taint{{Key: "hakopod.com/pool", Value: "actions", Effect: corev1.TaintEffectNoSchedule}}
	kube.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
	if err := p.verifyRuntime(context.Background()); err != nil {
		t.Fatal("owned pool rejected", err)
	}
	p.lock = actionsWorkspaceProbeLock{Nonce: probeTestInstallation, Namespace: "probe"}
	pod, err := p.desiredPod()
	if err != nil || pod.Spec.NodeSelector["hakopod.com/pool"] != "actions" || len(pod.Spec.Tolerations) != 3 || pod.Spec.Tolerations[0].Operator != corev1.TolerationOpEqual || pod.Spec.Tolerations[0].Value != "actions" {
		t.Fatal("probe lost exact pool placement", err)
	}
	for _, taint := range []corev1.Taint{
		{Key: "hakopod.com/pool", Value: "other", Effect: corev1.TaintEffectNoSchedule},
		{Key: "hakopod.com/pool", Value: "actions", Effect: corev1.TaintEffectNoExecute},
		{Key: "dedicated", Value: "other", Effect: corev1.TaintEffectNoSchedule},
	} {
		node.Spec.Taints = []corev1.Taint{taint}
		if _, err := actionsWorkspaceProbeTolerations(node); err == nil {
			t.Fatal("unrelated taint accepted")
		}
	}
}

func TestActionsWorkspaceProbeAccountingSelectsExactUIDAndRejectsDuplicates(t *testing.T) {
	p, _, _ := newWorkspaceProbeTest(t)
	volume := map[string]any{"name": "runner", "time": p.now(), "usedBytes": 123}
	pod := map[string]any{"podRef": map[string]string{"name": actionsWorkspaceProbePodName, "namespace": "probe", "uid": string(probeTestPodUID)}, "volume": []any{volume}}
	summary := map[string]any{"node": map[string]string{"nodeName": p.options.NodeName}, "pods": []any{pod}}
	raw, _ := json.Marshal(summary)
	if used, present, err := actionsWorkspaceVolumeUsage(raw, p.options.NodeName, "probe", probeTestPodUID, p.now()); err != nil || !present || used != 123 {
		t.Fatal("valid accounting rejected", err)
	}
	if _, present, err := actionsWorkspaceVolumeUsage(raw, p.options.NodeName, "probe", probeTestNamespaceUID, p.now()); err != nil || present {
		t.Fatal("accounting matched wrong UID")
	}
	summary["pods"] = []any{pod, pod}
	raw, _ = json.Marshal(summary)
	if _, _, err := actionsWorkspaceVolumeUsage(raw, p.options.NodeName, "probe", probeTestPodUID, p.now()); err == nil {
		t.Fatal("duplicate accounting accepted")
	}
	summary["pods"] = []any{pod}
	volume["time"] = p.now().Add(-4 * time.Minute)
	raw, _ = json.Marshal(summary)
	if _, _, err := actionsWorkspaceVolumeUsage(raw, p.options.NodeName, "probe", probeTestPodUID, p.now()); err == nil {
		t.Fatal("stale accounting accepted")
	}
}
