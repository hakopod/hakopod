package cluster

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

const HostAccessNamespace = "hakopod-host-access"
const hostSessionLabel = "hakopod.io/host-session"
const hostAccessLabel = "hakopod.io/host-access"
const hostTerminalImage = "docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0"

func (c *Client) HostNode(ctx context.Context, name string) (types.UID, error) {
	if name == "" || len(validation.IsDNS1123Subdomain(name)) > 0 {
		return "", fmt.Errorf("select a node")
	}
	node, err := c.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if node.DeletionTimestamp != nil || node.Status.NodeInfo.OperatingSystem != "linux" {
		return "", fmt.Errorf("host terminal requires an available Linux node")
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			return node.UID, nil
		}
	}
	return "", fmt.Errorf("node is not ready")
}

func (c *Client) hostNamespace(ctx context.Context) error {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, HostAccessNamespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		ns, err = c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: HostAccessNamespace, Labels: map[string]string{managedBy: "hakopod", hostAccessLabel: "true", "pod-security.kubernetes.io/enforce": "privileged"}}}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			ns, err = c.kube.CoreV1().Namespaces().Get(ctx, HostAccessNamespace, metav1.GetOptions{})
		}
	}
	if err != nil {
		return err
	}
	if ns.Labels[managedBy] != "hakopod" || ns.Labels[hostAccessLabel] != "true" || ns.DeletionTimestamp != nil {
		return fmt.Errorf("host access namespace ownership changed")
	}
	return nil
}

func hostJob(node, session string) *batchv1.Job {
	yes, no := true, false
	zero := int32(0)
	deadline, ttl, grace := int64(660), int32(60), int64(0)
	root := int64(0)
	hostType := corev1.HostPathDirectory
	labels := map[string]string{managedBy: "hakopod", hostAccessLabel: "true", hostSessionLabel: session}
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "terminal-" + session, Namespace: HostAccessNamespace, Labels: labels}, Spec: batchv1.JobSpec{
		BackoffLimit: &zero, ActiveDeadlineSeconds: &deadline, TTLSecondsAfterFinished: &ttl,
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
			NodeName: node, HostPID: true, HostNetwork: true, HostIPC: true, RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &no, TerminationGracePeriodSeconds: &grace,
			Tolerations: []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			Volumes:     []corev1.Volume{{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/", Type: &hostType}}}},
			Containers: []corev1.Container{{Name: "terminal", Image: hostTerminalImage, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/bin/sleep", "650"},
				SecurityContext: &corev1.SecurityContext{Privileged: &yes, RunAsUser: &root, ReadOnlyRootFilesystem: &yes},
				Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("128Mi")}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "host", MountPath: "/host"}},
			}},
		}},
	}}
}

// HostTerminal starts an expiring, exact-node job only after a client attaches.
// It never reads a host shell command from the opening request.
func (c *Client) HostTerminal(ctx context.Context, node, session string, uid types.UID, stdin io.Reader, stdout io.Writer, sizes remotecommand.TerminalSizeQueue) error {
	if len(session) != 32 || len(validation.IsDNS1123Label(session)) > 0 {
		return fmt.Errorf("invalid session")
	}
	current, err := c.HostNode(ctx, node)
	if err != nil {
		return err
	}
	if current != uid {
		return fmt.Errorf("node was replaced")
	}
	if c.execConfig == nil || c.restClient() == nil {
		return fmt.Errorf("interactive transport is unavailable")
	}
	if err = c.hostNamespace(ctx); err != nil {
		return err
	}
	job, err := c.kube.BatchV1().Jobs(HostAccessNamespace).Create(ctx, hostJob(node, session), metav1.CreateOptions{})
	if err != nil {
		return err
	}
	defer func() {
		clean, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		current, err := c.kube.BatchV1().Jobs(HostAccessNamespace).Get(clean, job.Name, metav1.GetOptions{})
		if err != nil || current.UID != job.UID || current.Labels[hostSessionLabel] != session || current.Labels[managedBy] != "hakopod" {
			return
		}
		policy := metav1.DeletePropagationForeground
		_ = c.kube.BatchV1().Jobs(HostAccessNamespace).Delete(clean, job.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &job.UID}, PropagationPolicy: &policy})
	}()
	wait, stop := context.WithTimeout(ctx, 90*time.Second)
	defer stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var pod *corev1.Pod
	for pod == nil {
		select {
		case <-wait.Done():
			return fmt.Errorf("host shell did not start before its deadline")
		case <-ticker.C:
		}
		pods, err := c.kube.CoreV1().Pods(HostAccessNamespace).List(wait, metav1.ListOptions{LabelSelector: hostSessionLabel + "=" + session, Limit: 2})
		if err != nil {
			return err
		}
		if len(pods.Items) > 1 {
			return fmt.Errorf("ambiguous host terminal workload")
		}
		for i := range pods.Items {
			candidate := &pods.Items[i]
			owner := metav1.GetControllerOf(candidate)
			if candidate.Spec.NodeName != node || candidate.Labels[managedBy] != "hakopod" || owner == nil || owner.UID != job.UID {
				return fmt.Errorf("host terminal ownership changed")
			}
			if candidate.Status.Phase == corev1.PodFailed {
				return fmt.Errorf("host shell could not start")
			}
			for _, status := range candidate.Status.ContainerStatuses {
				if status.Name == "terminal" && status.State.Running != nil {
					pod = candidate
				}
			}
		}
	}
	current, err = c.HostNode(ctx, node)
	if err != nil {
		return err
	}
	if current != uid {
		return fmt.Errorf("node was replaced")
	}
	if err = c.hostNamespace(ctx); err != nil {
		return err
	}
	latest, err := c.kube.CoreV1().Pods(HostAccessNamespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	controller := metav1.GetControllerOf(latest)
	if latest.UID != pod.UID || latest.DeletionTimestamp != nil || latest.Spec.NodeName != node || latest.Labels[hostSessionLabel] != session || controller == nil || controller.UID != job.UID {
		return fmt.Errorf("host terminal pod was replaced")
	}
	u := c.restClient().Post().Resource("pods").Namespace(HostAccessNamespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "terminal", Command: []string{"/bin/busybox", "chroot", "/host", "/bin/sh"}, Stdin: true, Stdout: true, TTY: true}, scheme.ParameterCodec).URL()
	exec, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		return err
	}
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Tty: true, TerminalSizeQueue: sizes})
}
