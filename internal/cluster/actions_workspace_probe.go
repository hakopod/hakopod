package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	// This annotation holds only in-flight ownership. The capability label is
	// the sole qualification record used to admit new runner pods.
	ActionsWorkspaceProbeLockAnnotation = "hakopod.io/actions-workspace-probe"
	actionsWorkspaceProbeOwnerLabel     = "hakopod.io/actions-workspace-probe-owner"
	actionsWorkspaceInstallationLabel   = "hakopod.com/installation"
	actionsWorkspaceProbePodName        = "workspace"
	actionsWorkspaceProbeBytes          = 16 << 20
	actionsWorkspaceProbeTimeout        = 15 * time.Minute
	actionsWorkspaceProbeCleanupTimeout = 90 * time.Second
)

var actionsWorkspaceProbeID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var actionsWorkspaceProbeUID = regexp.MustCompile(`^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

// ActionsWorkspaceProbeOptions is accepted only by the local operator command.
// It is deliberately absent from application configuration and the HTTP API.
type ActionsWorkspaceProbeOptions struct {
	Kubeconfig     string
	Context        string
	NodeName       string
	InstallationID string
	Profile        ActionsWorkspaceProfile
}

func (o ActionsWorkspaceProbeOptions) Validate() error {
	if !filepath.IsAbs(o.Kubeconfig) || len(o.Kubeconfig) > 4096 || strings.ContainsAny(o.Kubeconfig, "\x00\r\n") {
		return fmt.Errorf("workspace probe requires an absolute kubeconfig path")
	}
	if o.Context == "" || len(o.Context) > 253 || strings.ContainsAny(o.Context, "\x00\r\n") {
		return fmt.Errorf("workspace probe requires an exact Kubernetes context")
	}
	if o.NodeName == "" || len(validation.IsDNS1123Subdomain(o.NodeName)) != 0 || !actionsWorkspaceProbeID.MatchString(o.InstallationID) {
		return fmt.Errorf("workspace probe requires an exact node and installation ID")
	}
	if o.Profile != ActionsWorkspaceSharedOverlay2V1 {
		return fmt.Errorf("workspace probe supports only shared-overlay2-v1")
	}
	return nil
}

type actionsWorkspaceProbeLock struct {
	Version        int       `json:"version"`
	Nonce          string    `json:"nonce"`
	NodeUID        types.UID `json:"node_uid"`
	RuntimeUID     types.UID `json:"runtime_uid"`
	InstallationID string    `json:"installation_id"`
	Namespace      string    `json:"namespace"`
	NamespaceUID   types.UID `json:"namespace_uid,omitempty"`
	PodUID         types.UID `json:"pod_uid,omitempty"`
	KubeletDevice  uint64    `json:"kubelet_device"`
	KubeletInode   uint64    `json:"kubelet_inode"`
	Deadline       time.Time `json:"deadline"`
}

type actionsWorkspaceDiskSample struct {
	Name           string
	Device         uint64
	Inode          uint64
	LogicalBytes   uint64
	AllocatedBytes uint64
}

type actionsWorkspaceProbeHost interface {
	MachineID() (string, error)
	RootIdentity() (uint64, uint64, error)
	Observe(types.UID) (actionsWorkspaceDiskSample, error)
	Removed(types.UID) (bool, error)
}

type actionsWorkspaceProbe struct {
	kube       kubernetes.Interface
	execConfig *rest.Config
	host       actionsWorkspaceProbeHost
	options    ActionsWorkspaceProbeOptions
	node       *corev1.Node
	runtime    *nodev1.RuntimeClass
	lock       actionsWorkspaceProbeLock
	pod        *corev1.Pod
	now        func() time.Time
	wait       func(context.Context, time.Duration) error
	execute    func(context.Context, *corev1.Pod, string, []string) ([]byte, error)
	usage      func(context.Context) (uint64, bool, error)
}

// ProbeActionsWorkspace qualifies the local owned node. No credentials are
// copied into the disposable pod, and no capability is published before its
// Kubernetes resources and physical workspace have both been removed.
func ProbeActionsWorkspace(ctx context.Context, options ActionsWorkspaceProbeOptions) error {
	if err := options.Validate(); err != nil {
		return err
	}
	host, err := newActionsWorkspaceProbeHost()
	if err != nil {
		return err
	}
	if closer, ok := host.(io.Closer); ok {
		defer closer.Close()
	}
	file, err := os.Open(options.Kubeconfig)
	if err != nil {
		return fmt.Errorf("workspace probe kubeconfig is unavailable")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	file.Close()
	if readErr != nil || len(raw) > 1<<20 {
		return fmt.Errorf("workspace probe kubeconfig exceeds its read bound")
	}
	loaded, err := clientcmd.Load(raw)
	if err != nil || loaded.Contexts[options.Context] == nil {
		return fmt.Errorf("workspace probe Kubernetes context is unavailable")
	}
	for _, cluster := range loaded.Clusters {
		if cluster == nil {
			return fmt.Errorf("workspace probe Kubernetes cluster configuration is invalid")
		}
		cluster.LocationOfOrigin = options.Kubeconfig
	}
	for _, auth := range loaded.AuthInfos {
		if auth == nil {
			return fmt.Errorf("workspace probe Kubernetes authentication configuration is invalid")
		}
		auth.LocationOfOrigin = options.Kubeconfig
	}
	if err := clientcmd.ResolveLocalPaths(loaded); err != nil {
		return fmt.Errorf("workspace probe Kubernetes file references are invalid")
	}
	config, err := clientcmd.NewNonInteractiveClientConfig(*loaded, options.Context, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return fmt.Errorf("workspace probe Kubernetes configuration is invalid")
	}
	config.QPS, config.Burst, config.Timeout = 5, 10, 15*time.Second
	config.UserAgent = "hakopod/actions-workspace-probe-v1"
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("workspace probe Kubernetes client is unavailable")
	}
	execConfig := rest.CopyConfig(config)
	execConfig.Timeout = 0
	p := &actionsWorkspaceProbe{kube: kube, execConfig: execConfig, host: host, options: options, now: time.Now, wait: actionsWorkspaceProbeWait}
	p.execute = p.exec
	p.usage = p.volumeUsage
	ctx, cancel := context.WithTimeout(ctx, actionsWorkspaceProbeTimeout)
	defer cancel()
	return p.run(ctx)
}

func actionsWorkspaceProbeWait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *actionsWorkspaceProbe) ownedNode(ctx context.Context) (*corev1.Node, error) {
	node, err := p.kube.CoreV1().Nodes().Get(ctx, p.options.NodeName, metav1.GetOptions{})
	if err != nil || node.UID == "" || node.DeletionTimestamp != nil || node.Labels[actionsWorkspaceInstallationLabel] != p.options.InstallationID {
		return nil, fmt.Errorf("workspace probe node ownership could not be verified")
	}
	if p.node != nil && node.UID != p.node.UID {
		return nil, fmt.Errorf("workspace probe node identity changed")
	}
	return node, nil
}

func (p *actionsWorkspaceProbe) verifyRuntime(ctx context.Context) error {
	node, err := p.ownedNode(ctx)
	if err != nil {
		return err
	}
	machineID, err := p.host.MachineID()
	if err != nil || !actionsWorkspaceProbeID.MatchString(machineID) || node.Status.NodeInfo.MachineID != machineID || node.Status.NodeInfo.BootID == "" || node.Status.NodeInfo.Architecture != runtime.GOARCH || node.Labels["kubernetes.io/arch"] != runtime.GOARCH || node.Labels["hakopod.io/actions-runtime"] != "ready" || node.Spec.Unschedulable {
		return fmt.Errorf("workspace probe must run on its exact ready local Actions node")
	}
	ready := false
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			ready = true
		}
		if (condition.Type == corev1.NodeDiskPressure || condition.Type == corev1.NodeMemoryPressure || condition.Type == corev1.NodePIDPressure) && condition.Status == corev1.ConditionTrue {
			return fmt.Errorf("workspace probe node has resource pressure")
		}
	}
	if !ready || (p.node != nil && !reflect.DeepEqual(p.node.Status.NodeInfo, node.Status.NodeInfo)) {
		return fmt.Errorf("workspace probe node is not ready or its runtime identity changed")
	}
	tolerations, err := actionsWorkspaceProbeTolerations(node)
	if err != nil {
		return err
	}
	if p.node != nil {
		original, originalErr := actionsWorkspaceProbeTolerations(p.node)
		if originalErr != nil || !reflect.DeepEqual(original, tolerations) || node.Labels["hakopod.com/pool"] != p.node.Labels["hakopod.com/pool"] {
			return fmt.Errorf("workspace probe node pool or taints changed")
		}
	}
	rc, err := p.kube.NodeV1().RuntimeClasses().Get(ctx, ActionsRuntime, metav1.GetOptions{})
	if err != nil || rc.UID == "" || rc.DeletionTimestamp != nil || rc.Labels[managedBy] != "hakopod" || rc.Labels[actionsWorkspaceInstallationLabel] != p.options.InstallationID || rc.Handler != ActionsRuntime || rc.Scheduling == nil || !reflect.DeepEqual(rc.Scheduling.NodeSelector, map[string]string{"hakopod.io/actions-runtime": "ready"}) || len(rc.Scheduling.Tolerations) != 0 || rc.Overhead == nil || len(rc.Overhead.PodFixed) != 2 {
		return fmt.Errorf("workspace probe Actions RuntimeClass ownership or profile is invalid")
	}
	cpu, memory := rc.Overhead.PodFixed[corev1.ResourceCPU], rc.Overhead.PodFixed[corev1.ResourceMemory]
	if cpu.Cmp(resource.MustParse("100m")) != 0 || memory.Cmp(resource.MustParse("512Mi")) != 0 || (p.runtime != nil && rc.UID != p.runtime.UID) {
		return fmt.Errorf("workspace probe Actions RuntimeClass identity or overhead changed")
	}
	if p.node == nil {
		p.node = node
	}
	if p.runtime == nil {
		p.runtime = rc
	}
	return nil
}

func parseActionsWorkspaceProbeLock(raw string) (actionsWorkspaceProbeLock, error) {
	var lock actionsWorkspaceProbeLock
	if len(raw) == 0 || len(raw) > 2048 {
		return lock, fmt.Errorf("workspace probe lock is invalid; inspect the owned node")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&lock) != nil || lock.Version != 1 || !actionsWorkspaceProbeID.MatchString(lock.Nonce) || !actionsWorkspaceProbeID.MatchString(lock.InstallationID) || lock.NodeUID == "" || lock.RuntimeUID == "" || lock.Namespace != "hakopod-workspace-probe-"+lock.Nonce || lock.Deadline.IsZero() || lock.KubeletInode == 0 || (lock.NamespaceUID != "" && !actionsWorkspaceProbeUID.MatchString(string(lock.NamespaceUID))) || (lock.PodUID != "" && !actionsWorkspaceProbeUID.MatchString(string(lock.PodUID))) {
		return lock, fmt.Errorf("workspace probe lock is invalid; inspect the owned node")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return lock, fmt.Errorf("workspace probe lock has trailing data")
	}
	return lock, nil
}

func (p *actionsWorkspaceProbe) changeLock(ctx context.Context, expected string, next *actionsWorkspaceProbeLock, qualify bool) error {
	for attempt := 0; attempt < 3; attempt++ {
		node, err := p.ownedNode(ctx)
		if err != nil {
			return err
		}
		if node.Annotations[ActionsWorkspaceProbeLockAnnotation] != expected {
			return fmt.Errorf("workspace probe node lock changed")
		}
		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		delete(node.Labels, ActionsWorkspaceCapabilityLabel)
		if qualify {
			if err := p.verifyRoot(); err != nil {
				return err
			}
			if err := p.verifyRuntime(ctx); err != nil {
				return err
			}
			node.Labels[ActionsWorkspaceCapabilityLabel] = string(p.options.Profile)
		}
		if node.Annotations == nil {
			node.Annotations = map[string]string{}
		}
		delete(node.Annotations, ActionsWorkspaceProbeLockAnnotation)
		if next != nil {
			raw, _ := json.Marshal(next)
			node.Annotations[ActionsWorkspaceProbeLockAnnotation] = string(raw)
		}
		_, err = p.kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{FieldManager: "hakopod-actions-workspace-probe"})
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("workspace probe node update failed")
		}
		return nil
	}
	return fmt.Errorf("workspace probe node kept changing; retry after other maintenance completes")
}

func (p *actionsWorkspaceProbe) lockText() string {
	raw, _ := json.Marshal(p.lock)
	return string(raw)
}

func (p *actionsWorkspaceProbe) recordIdentity(ctx context.Context, namespaceUID, podUID types.UID) error {
	previous := p.lockText()
	next := p.lock
	if namespaceUID != "" {
		next.NamespaceUID = namespaceUID
	}
	if podUID != "" {
		next.PodUID = podUID
	}
	if err := p.changeLock(ctx, previous, &next, false); err != nil {
		return err
	}
	p.lock = next
	return nil
}

func (p *actionsWorkspaceProbe) acquire(ctx context.Context) (bool, error) {
	node, err := p.ownedNode(ctx)
	if err != nil {
		return false, err
	}
	if raw := node.Annotations[ActionsWorkspaceProbeLockAnnotation]; raw != "" {
		lock, err := parseActionsWorkspaceProbeLock(raw)
		if err != nil {
			return false, err
		}
		if lock.NodeUID != node.UID || lock.InstallationID != p.options.InstallationID {
			return false, fmt.Errorf("workspace probe lock belongs to another node or installation")
		}
		if p.now().Before(lock.Deadline.Add(actionsWorkspaceProbeCleanupTimeout)) {
			return false, fmt.Errorf("another workspace probe owns this node; let it finish cleanup")
		}
		p.lock = lock
		cleanupCtx, cancel := context.WithTimeout(context.Background(), actionsWorkspaceProbeCleanupTimeout)
		defer cancel()
		// Recovery retains the old ownership token. It never starts a new pod
		// or publishes a capability in the same invocation.
		if err := p.changeLock(cleanupCtx, raw, &lock, false); err != nil {
			return false, err
		}
		if err := p.cleanup(cleanupCtx); err != nil {
			return false, fmt.Errorf("previous workspace probe cleanup is incomplete; preserve its lock and retry cleanup")
		}
		if err := p.changeLock(cleanupCtx, p.lockText(), nil, false); err != nil {
			return false, err
		}
		return false, fmt.Errorf("previous workspace probe cleanup completed; run the command again to qualify the node")
	}
	// An owned node must lose stale eligibility even when the new probe's
	// local runtime preflight fails. Do not overwrite another probe's lock.
	if err := p.changeLock(ctx, "", nil, false); err != nil {
		return false, err
	}
	if err := p.verifyRuntime(ctx); err != nil {
		return false, err
	}
	device, inode, err := p.host.RootIdentity()
	if err != nil || inode == 0 {
		return false, fmt.Errorf("workspace probe kubelet filesystem identity is unavailable")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return false, fmt.Errorf("workspace probe ownership token could not be generated")
	}
	p.lock = actionsWorkspaceProbeLock{Version: 1, Nonce: hex.EncodeToString(nonce), NodeUID: node.UID, RuntimeUID: p.runtime.UID, InstallationID: p.options.InstallationID, KubeletDevice: device, KubeletInode: inode, Deadline: p.now().Add(actionsWorkspaceProbeTimeout)}
	p.lock.Namespace = "hakopod-workspace-probe-" + p.lock.Nonce
	if err := p.changeLock(ctx, "", &p.lock, false); err != nil {
		return false, err
	}
	return true, nil
}

func (p *actionsWorkspaceProbe) run(ctx context.Context) (result error) {
	// Capture the owned node first. Even an expired lock is never followed to
	// another node, installation or unowned namespace during cleanup recovery.
	node, err := p.ownedNode(ctx)
	if err != nil {
		return err
	}
	p.node = node
	machineID, machineErr := p.host.MachineID()
	if machineErr != nil || node.Status.NodeInfo.MachineID != machineID || !actionsWorkspaceProbeID.MatchString(machineID) {
		if node.Annotations[ActionsWorkspaceProbeLockAnnotation] == "" {
			if err := p.changeLock(ctx, "", nil, false); err != nil {
				return err
			}
		}
		return fmt.Errorf("workspace probe must run on the selected node's local filesystem")
	}
	acquired, err := p.acquire(ctx)
	if err != nil || !acquired {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), actionsWorkspaceProbeCleanupTimeout)
		defer cancel()
		if err := p.cleanup(cleanupCtx); err != nil {
			result = errors.Join(result, fmt.Errorf("workspace probe cleanup is incomplete; capability remains disabled and its lock is retained for cleanup retry"))
			return
		}
		qualify := result == nil && ctx.Err() == nil
		if err := p.changeLock(cleanupCtx, p.lockText(), nil, qualify); err != nil {
			result = errors.Join(result, err)
		}
		if result == nil && !qualify {
			result = fmt.Errorf("workspace probe was cancelled; capability remains disabled")
		}
	}()
	if err := p.create(ctx); err != nil {
		return err
	}
	return p.prove(ctx)
}

func (p *actionsWorkspaceProbe) ownershipLabels() map[string]string {
	return map[string]string{managedBy: "hakopod", actionsWorkspaceInstallationLabel: p.options.InstallationID, actionsWorkspaceProbeOwnerLabel: p.lock.Nonce}
}

func (p *actionsWorkspaceProbe) owns(meta metav1.Object) bool {
	labels := meta.GetLabels()
	return meta.GetUID() != "" && labels[managedBy] == "hakopod" && labels[actionsWorkspaceInstallationLabel] == p.options.InstallationID && labels[actionsWorkspaceProbeOwnerLabel] == p.lock.Nonce
}

func (p *actionsWorkspaceProbe) desiredPod() (*corev1.Pod, error) {
	svc := spec.Service{Architecture: runtime.GOARCH, NodeName: p.options.NodeName, Resources: &spec.Resources{CPURequest: "1", CPULimit: "1", MemoryRequest: "2Gi", MemoryLimit: "2Gi"}, Actions: &spec.Actions{WorkspaceSizeGiB: 2, TimeoutMinutes: 12}}
	pod := actionsPod(Target{ApplicationID: "workspace-probe"}, "runner", p.lock.Nonce, svc)
	if err := applyActionsWorkspaceProfile(pod, 2, p.options.Profile); err != nil {
		return nil, err
	}
	pod.Name, pod.Namespace, pod.Labels = actionsWorkspaceProbePodName, p.lock.Namespace, p.ownershipLabels()
	pod.Spec.Volumes = pod.Spec.Volumes[:1]
	// RuntimeClass admission adds these fields. State them explicitly so the
	// admitted pod can be checked against its exact expected resource envelope.
	pod.Spec.NodeSelector["hakopod.io/actions-runtime"] = "ready"
	pod.Spec.Overhead = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("512Mi")}
	tolerations, err := actionsWorkspaceProbeTolerations(p.node)
	if err != nil {
		return nil, err
	}
	pod.Spec.Tolerations = append(tolerations,
		corev1.Toleration{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: ptr(int64(300))},
		corev1.Toleration{Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: ptr(int64(300))})
	if p.node != nil && len(tolerations) == 1 {
		pod.Spec.NodeSelector["hakopod.com/pool"] = p.node.Labels["hakopod.com/pool"]
	}
	pod.Spec.Containers[0].VolumeMounts = pod.Spec.Containers[0].VolumeMounts[:1]
	pod.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "DOCKER_HOST", Value: "tcp://127.0.0.1:2375"}}
	pod.Spec.Containers[0].Command = []string{"/usr/bin/python3", "-u", "-c", actionsWorkspaceStartupGuard + "\nfrom pathlib import Path\nimport time\nPath('/home/runner/.hakopod-workspace-probe-ready').touch(exist_ok=False)\ntime.sleep(700)\n"}
	return pod, nil
}

func actionsWorkspaceProbeTolerations(node *corev1.Node) ([]corev1.Toleration, error) {
	if node == nil {
		return nil, nil
	}
	if len(node.Spec.Taints) > 16 {
		return nil, fmt.Errorf("workspace probe node taints exceed their bound")
	}
	var result []corev1.Toleration
	for _, taint := range node.Spec.Taints {
		pool := node.Labels["hakopod.com/pool"]
		if len(result) != 0 || pool == "" || taint.Key != "hakopod.com/pool" || taint.Value != pool || taint.Effect != corev1.TaintEffectNoSchedule {
			return nil, fmt.Errorf("workspace probe node has an unsupported or mismatched scheduling taint")
		}
		result = append(result, corev1.Toleration{Key: taint.Key, Value: pool, Operator: corev1.TolerationOpEqual, Effect: taint.Effect})
	}
	return result, nil
}

func (p *actionsWorkspaceProbe) create(ctx context.Context) error {
	ns, err := p.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: p.lock.Namespace, Labels: p.ownershipLabels()}}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("workspace probe namespace could not be created")
	}
	if !p.owns(ns) || !actionsWorkspaceProbeUID.MatchString(string(ns.UID)) {
		return fmt.Errorf("workspace probe namespace identity is invalid")
	}
	if err := p.recordIdentity(ctx, ns.UID, ""); err != nil {
		return err
	}
	wanted, err := p.desiredPod()
	if err != nil {
		return err
	}
	pod, err := p.kube.CoreV1().Pods(ns.Name).Create(ctx, wanted, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("workspace probe pod could not be created")
	}
	if !p.owns(pod) || !actionsWorkspaceProbeUID.MatchString(string(pod.UID)) || !actionsWorkspaceProbeShape(pod, wanted) {
		return fmt.Errorf("workspace probe pod identity or security envelope changed at admission")
	}
	p.pod = pod
	if err := p.recordIdentity(ctx, ns.UID, pod.UID); err != nil {
		return err
	}
	startCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	for {
		current, err := p.currentPod(startCtx)
		if err != nil {
			return err
		}
		if current.Status.Phase == corev1.PodRunning && current.Spec.NodeName == p.options.NodeName {
			return nil
		}
		if current.Status.Phase == corev1.PodFailed || current.Status.Phase == corev1.PodSucceeded {
			return fmt.Errorf("workspace probe pod stopped before qualification")
		}
		if err := p.wait(startCtx, 2*time.Second); err != nil {
			return fmt.Errorf("workspace probe pod did not start within its time bound")
		}
	}
}

func actionsWorkspaceProbeShape(got, wanted *corev1.Pod) bool {
	if got == nil || wanted == nil || got.Name != wanted.Name || got.Namespace != wanted.Namespace || !reflect.DeepEqual(got.Annotations, wanted.Annotations) || !reflect.DeepEqual(got.Spec.RuntimeClassName, wanted.Spec.RuntimeClassName) || !reflect.DeepEqual(got.Spec.AutomountServiceAccountToken, wanted.Spec.AutomountServiceAccountToken) || !reflect.DeepEqual(got.Spec.SecurityContext, wanted.Spec.SecurityContext) || !reflect.DeepEqual(got.Spec.ActiveDeadlineSeconds, wanted.Spec.ActiveDeadlineSeconds) || !reflect.DeepEqual(got.Spec.Volumes, wanted.Spec.Volumes) || !reflect.DeepEqual(got.Spec.Affinity, wanted.Spec.Affinity) || !reflect.DeepEqual(got.Spec.NodeSelector, wanted.Spec.NodeSelector) || !reflect.DeepEqual(got.Spec.Overhead, wanted.Spec.Overhead) || !reflect.DeepEqual(got.Spec.Tolerations, wanted.Spec.Tolerations) || got.Spec.HostNetwork || got.Spec.HostPID || got.Spec.HostIPC || got.Spec.RestartPolicy != wanted.Spec.RestartPolicy || len(got.Spec.InitContainers) != 2 || len(got.Spec.Containers) != 1 || len(got.Spec.EphemeralContainers) != 0 {
		return false
	}
	containers := append(append([]corev1.Container{}, got.Spec.InitContainers...), got.Spec.Containers...)
	expected := append(append([]corev1.Container{}, wanted.Spec.InitContainers...), wanted.Spec.Containers...)
	for i, c := range containers {
		w := expected[i]
		if c.Name != w.Name || c.Image != w.Image || !reflect.DeepEqual(c.Command, w.Command) || !reflect.DeepEqual(c.Args, w.Args) || !reflect.DeepEqual(c.Env, w.Env) || len(c.EnvFrom) != 0 || !reflect.DeepEqual(c.SecurityContext, w.SecurityContext) || !reflect.DeepEqual(c.Resources, w.Resources) || !reflect.DeepEqual(c.VolumeMounts, w.VolumeMounts) || len(c.VolumeDevices) != 0 || !reflect.DeepEqual(c.RestartPolicy, w.RestartPolicy) {
			return false
		}
	}
	return true
}

func (p *actionsWorkspaceProbe) currentPod(ctx context.Context) (*corev1.Pod, error) {
	if err := p.verifyRuntime(ctx); err != nil {
		return nil, err
	}
	node, err := p.ownedNode(ctx)
	if err != nil || node.Annotations[ActionsWorkspaceProbeLockAnnotation] != p.lockText() {
		return nil, fmt.Errorf("workspace probe no longer owns the node lock")
	}
	ns, err := p.kube.CoreV1().Namespaces().Get(ctx, p.lock.Namespace, metav1.GetOptions{})
	if err != nil || !p.owns(ns) || ns.UID != p.lock.NamespaceUID || ns.DeletionTimestamp != nil {
		return nil, fmt.Errorf("workspace probe namespace identity changed")
	}
	pod, err := p.kube.CoreV1().Pods(ns.Name).Get(ctx, actionsWorkspaceProbePodName, metav1.GetOptions{})
	if err != nil || !p.owns(pod) || pod.UID != p.lock.PodUID || pod.DeletionTimestamp != nil || (pod.Spec.NodeName != "" && pod.Spec.NodeName != p.options.NodeName) || !actionsWorkspaceProbeShape(pod, p.pod) {
		return nil, fmt.Errorf("workspace probe pod identity or security envelope changed")
	}
	return pod, nil
}

type actionsWorkspaceProbeBuffer struct{ bytes.Buffer }

func (b *actionsWorkspaceProbeBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 8192 {
		return 0, fmt.Errorf("workspace probe output exceeded its bound")
	}
	return b.Buffer.Write(data)
}

func (p *actionsWorkspaceProbe) exec(ctx context.Context, pod *corev1.Pod, container string, command []string) ([]byte, error) {
	request := p.kube.CoreV1().RESTClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: command, Stdout: true, Stderr: true, TTY: false}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(p.execConfig, http.MethodPost, request.URL())
	if err != nil {
		return nil, fmt.Errorf("workspace probe execution transport is unavailable")
	}
	var stdout, stderr actionsWorkspaceProbeBuffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr, Tty: false})
	if err != nil {
		// Container output is diagnostic data and may include network responses.
		// Never copy it into installer logs or a returned error.
		return nil, fmt.Errorf("workspace probe command did not complete")
	}
	return stdout.Bytes(), nil
}

func (p *actionsWorkspaceProbe) command(ctx context.Context, container string, command ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 70*time.Second)
	defer cancel()
	pod, err := p.currentPod(ctx)
	if err != nil {
		return nil, err
	}
	if pod.Status.Phase != corev1.PodRunning || pod.Spec.NodeName != p.options.NodeName {
		return nil, fmt.Errorf("workspace probe pod is not running on the selected node")
	}
	return p.execute(ctx, pod, container, command)
}

func (p *actionsWorkspaceProbe) python(ctx context.Context, code string) ([]byte, error) {
	return p.command(ctx, "runner", "/usr/bin/python3", "-c", code)
}

func (p *actionsWorkspaceProbe) volumeUsage(ctx context.Context) (uint64, bool, error) {
	stream, err := p.kube.CoreV1().RESTClient().Get().AbsPath("/api/v1/nodes/" + p.options.NodeName + "/proxy/stats/summary").Stream(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("workspace probe kubelet accounting is unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(stream, (2<<20)+1))
	stream.Close()
	if err != nil || len(raw) > 2<<20 {
		return 0, false, fmt.Errorf("workspace probe kubelet accounting exceeded its read bound")
	}
	return actionsWorkspaceVolumeUsage(raw, p.options.NodeName, p.lock.Namespace, p.lock.PodUID, p.now())
}

func actionsWorkspaceVolumeUsage(raw []byte, node, namespace string, uid types.UID, now time.Time) (uint64, bool, error) {
	var summary struct {
		Node struct {
			Name string `json:"nodeName"`
		} `json:"node"`
		Pods []struct {
			Ref     struct{ Name, Namespace, UID string } `json:"podRef"`
			Volumes []struct {
				Name string    `json:"name"`
				Time time.Time `json:"time"`
				Used *uint64   `json:"usedBytes"`
			} `json:"volume"`
		} `json:"pods"`
	}
	if len(raw) > 2<<20 || json.Unmarshal(raw, &summary) != nil || summary.Node.Name != node || len(summary.Pods) > 512 {
		return 0, false, fmt.Errorf("workspace probe kubelet accounting is malformed")
	}
	found, used, present := 0, uint64(0), false
	for _, pod := range summary.Pods {
		if pod.Ref.UID != string(uid) || pod.Ref.Namespace != namespace || pod.Ref.Name != actionsWorkspaceProbePodName {
			continue
		}
		found++
		if found > 1 || len(pod.Volumes) > 32 {
			return 0, false, fmt.Errorf("workspace probe kubelet accounting has duplicate or excessive records")
		}
		for _, volume := range pod.Volumes {
			if volume.Name != "runner" {
				continue
			}
			if present || volume.Used == nil || *volume.Used > 64<<30 || volume.Time.IsZero() || now.Sub(volume.Time) > 3*time.Minute || volume.Time.After(now.Add(10*time.Second)) {
				return 0, false, fmt.Errorf("workspace probe kubelet workspace accounting is invalid")
			}
			used, present = *volume.Used, true
		}
	}
	return used, present, nil
}

func actionsWorkspaceByteDifference(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

func (p *actionsWorkspaceProbe) accounted(ctx context.Context, previous *actionsWorkspaceDiskSample, ready func(actionsWorkspaceDiskSample, uint64) bool) (actionsWorkspaceDiskSample, uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, 135*time.Second)
	defer cancel()
	for attempt := 0; attempt < 48; attempt++ {
		if _, err := p.currentPod(ctx); err != nil {
			return actionsWorkspaceDiskSample{}, 0, err
		}
		if err := p.verifyRoot(); err != nil {
			return actionsWorkspaceDiskSample{}, 0, err
		}
		disk, err := p.host.Observe(p.lock.PodUID)
		if err != nil {
			return disk, 0, fmt.Errorf("workspace probe disk backing could not be verified")
		}
		if previous != nil && (disk.Name != previous.Name || disk.Device != previous.Device || disk.Inode != previous.Inode) {
			return disk, 0, fmt.Errorf("workspace probe disk backing identity changed")
		}
		used, present, err := p.usage(ctx)
		if err != nil {
			return disk, 0, err
		}
		if present && actionsWorkspaceByteDifference(used, disk.AllocatedBytes) <= 1<<20 && (ready == nil || ready(disk, used)) {
			return disk, used, nil
		}
		if err := p.wait(ctx, 3*time.Second); err != nil {
			break
		}
	}
	return actionsWorkspaceDiskSample{}, 0, fmt.Errorf("workspace probe disk allocation did not match kubelet accounting within its time bound")
}

func (p *actionsWorkspaceProbe) prove(ctx context.Context) error {
	inode, err := p.python(ctx, actionsWorkspaceProbeSharing)
	if err != nil || !regexp.MustCompile(`^[0-9]{1,20}\n$`).Match(inode) {
		return fmt.Errorf("workspace probe shared marker or nested overlay2 execution failed")
	}
	daemonInode, err := p.command(ctx, "docker", "sh", "-c", "test \"$(cat /home/runner/.hakopod-shared-prepare)\" = shared-workspace-v1 && test \"$(cat /home/runner/.hakopod-workspace-proof/runner)\" = runner-to-docker && test \"$(cat /home/runner/.hakopod-workspace-proof/docker)\" = docker-to-runner && stat -c %i /home/runner/.hakopod-workspace-proof/runner")
	if err != nil || !bytes.Equal(inode, daemonInode) {
		return fmt.Errorf("workspace probe Docker sidecar did not share its workspace")
	}
	before, beforeUsed, err := p.accounted(ctx, nil, nil)
	if err != nil {
		return err
	}
	if _, err := p.python(ctx, "import os\nwith open('/home/runner/.hakopod-workspace-proof/allocation', 'xb') as f:\n for _ in range(16): f.write(os.urandom(1024*1024))\n f.flush()\n os.fsync(f.fileno())\n"); err != nil {
		return fmt.Errorf("workspace probe bounded allocation failed")
	}
	after, afterUsed, err := p.accounted(ctx, &before, func(disk actionsWorkspaceDiskSample, used uint64) bool {
		return disk.AllocatedBytes >= before.AllocatedBytes+actionsWorkspaceProbeBytes && used >= beforeUsed+actionsWorkspaceProbeBytes-(1<<20)
	})
	if err != nil {
		return err
	}
	if after.AllocatedBytes < before.AllocatedBytes+actionsWorkspaceProbeBytes || afterUsed < beforeUsed+actionsWorkspaceProbeBytes-(1<<20) || actionsWorkspaceByteDifference(afterUsed-beforeUsed, after.AllocatedBytes-before.AllocatedBytes) > 1<<20 {
		return fmt.Errorf("workspace probe physical allocation growth was not accounted by kubelet")
	}
	if _, err := p.python(ctx, "import os\nos.unlink('/home/runner/.hakopod-workspace-proof/allocation')\n"); err != nil {
		return fmt.Errorf("workspace probe allocation could not be removed")
	}
	reclaimed, _, err := p.accounted(ctx, &before, func(disk actionsWorkspaceDiskSample, _ uint64) bool {
		return disk.AllocatedBytes <= after.AllocatedBytes-actionsWorkspaceProbeBytes
	})
	if err != nil {
		return err
	}
	if reclaimed.AllocatedBytes > after.AllocatedBytes-actionsWorkspaceProbeBytes {
		return fmt.Errorf("workspace probe disk allocation was not reclaimed")
	}
	return nil
}

const actionsWorkspaceProbeSharing = `import os, pathlib, subprocess, time
root = pathlib.Path('/home/runner')
for _ in range(30):
    if (root / '.hakopod-workspace-probe-ready').is_file():
        break
    time.sleep(1)
else:
    raise RuntimeError('workspace startup guard did not finish')
assert os.getuid() == 1001 and os.getgid() == 1001
assert (root / '.hakopod-shared-prepare').read_text() == 'shared-workspace-v1\n'
proof = root / '.hakopod-workspace-proof'
proof.mkdir(mode=0o770)
(proof / 'runner').write_text('runner-to-docker')
subprocess.run(['docker', 'run', '--rm', '--name', 'hakopod-workspace-proof', '--mount', 'type=bind,source='+str(proof)+',target=/proof', 'docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0', 'sh', '-c', 'test "$(cat /proof/runner)" = runner-to-docker && printf docker-to-runner > /proof/docker && stat -c %i /proof/runner > /proof/inode'], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=35)
assert (proof / 'docker').read_text() == 'docker-to-runner'
assert (proof / 'inode').read_text().strip() == str((proof / 'runner').stat().st_ino)
print((proof / 'runner').stat().st_ino)
`

func (p *actionsWorkspaceProbe) cleanup(ctx context.Context) error {
	if err := p.verifyRoot(); err != nil {
		return err
	}
	node, err := p.ownedNode(ctx)
	if err != nil || node.Annotations[ActionsWorkspaceProbeLockAnnotation] != p.lockText() {
		return fmt.Errorf("workspace probe cleanup does not own its node lock")
	}
	ns, err := p.kube.CoreV1().Namespaces().Get(ctx, p.lock.Namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if p.lock.PodUID == "" {
			if p.lock.NamespaceUID != "" {
				return fmt.Errorf("workspace probe namespace vanished before its pod identity was recorded")
			}
			return nil
		}
		return p.waitRemoved(ctx, p.lock.PodUID)
	}
	if err != nil || !p.owns(ns) || (p.lock.NamespaceUID != "" && ns.UID != p.lock.NamespaceUID) {
		return fmt.Errorf("workspace probe cleanup namespace ownership changed")
	}
	pod, err := p.kube.CoreV1().Pods(ns.Name).Get(ctx, actionsWorkspaceProbePodName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("workspace probe cleanup pod could not be inspected")
	}
	uid := p.lock.PodUID
	if err == nil {
		if !p.owns(pod) || !actionsWorkspaceProbeUID.MatchString(string(pod.UID)) || (uid != "" && pod.UID != uid) || (pod.Spec.NodeName != "" && pod.Spec.NodeName != p.options.NodeName) {
			return fmt.Errorf("workspace probe cleanup pod ownership changed")
		}
		uid = pod.UID
		if p.lock.NamespaceUID == "" || p.lock.PodUID == "" {
			if err := p.recordIdentity(ctx, ns.UID, uid); err != nil {
				return err
			}
		}
		if err := p.kube.CoreV1().Pods(ns.Name).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("workspace probe pod deletion failed")
		}
	}
	for uid != "" {
		current, err := p.kube.CoreV1().Pods(ns.Name).Get(ctx, actionsWorkspaceProbePodName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			break
		}
		if err != nil || current.UID != uid {
			return fmt.Errorf("workspace probe pod identity changed during cleanup")
		}
		if err := p.wait(ctx, time.Second); err != nil {
			return fmt.Errorf("workspace probe pod cleanup timed out")
		}
	}
	if uid != "" {
		if err := p.waitRemoved(ctx, uid); err != nil {
			return err
		}
	}
	// Never delete another contributor's objects that appeared in this namespace.
	pods, err := p.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{Limit: 2})
	if err != nil || len(pods.Items) != 0 || pods.Continue != "" {
		return fmt.Errorf("workspace probe namespace contains an unexpected pod")
	}
	if err := p.kube.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &ns.UID}}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("workspace probe namespace deletion failed")
	}
	for {
		current, err := p.kube.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil || current.UID != ns.UID {
			return fmt.Errorf("workspace probe namespace identity changed during cleanup")
		}
		if err := p.wait(ctx, time.Second); err != nil {
			return fmt.Errorf("workspace probe namespace cleanup timed out")
		}
	}
}

func (p *actionsWorkspaceProbe) waitRemoved(ctx context.Context, uid types.UID) error {
	for {
		if err := p.verifyRoot(); err != nil {
			return err
		}
		removed, err := p.host.Removed(uid)
		if err != nil {
			return fmt.Errorf("workspace probe physical cleanup could not be verified")
		}
		if removed {
			return nil
		}
		if err := p.wait(ctx, time.Second); err != nil {
			return fmt.Errorf("workspace probe physical workspace remains after pod deletion")
		}
	}
}

func (p *actionsWorkspaceProbe) verifyRoot() error {
	device, inode, err := p.host.RootIdentity()
	if err != nil || inode == 0 || device != p.lock.KubeletDevice || inode != p.lock.KubeletInode {
		return fmt.Errorf("workspace probe kubelet filesystem identity changed")
	}
	return nil
}
