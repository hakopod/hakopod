package cluster

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

const managedBackupImage = "postgres:17.11-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73"
const installationLabel = "hakopod.com/installation"

// ManagedBackupPod is reserved for explicitly configured installer mode. It
// verifies the owned namespace/deployment/replicaset/pod chain and the pinned
// PostgreSQL client image, never an arbitrary system pod.
func (c *Client) ManagedBackupPod(ctx context.Context) (string, types.UID, string, error) {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, "hakopod-system", metav1.GetOptions{})
	if err != nil {
		return "", "", "", err
	}
	id := ns.Labels[installationLabel]
	if ns.Labels[managedBy] != "hakopod" || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(id) {
		return "", "", "", fmt.Errorf("management namespace is not owned by the installer")
	}
	deployment, err := c.kube.AppsV1().Deployments(ns.Name).Get(ctx, "postgres", metav1.GetOptions{})
	if err != nil {
		return "", "", "", err
	}
	if deployment.Labels[managedBy] != "hakopod" || deployment.Labels[installationLabel] != id || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		return "", "", "", fmt.Errorf("management database deployment ownership changed")
	}
	pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: "app=hakopod-postgres," + installationLabel + "=" + id, Limit: 2})
	if err != nil {
		return "", "", "", err
	}
	if len(pods.Items) != 1 || pods.Continue != "" {
		return "", "", "", fmt.Errorf("management backup requires one database pod")
	}
	pod := pods.Items[0]
	if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil || len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Kind != "ReplicaSet" || pod.OwnerReferences[0].Controller == nil || !*pod.OwnerReferences[0].Controller {
		return "", "", "", fmt.Errorf("management database pod is not running with a valid owner")
	}
	rs, err := c.kube.AppsV1().ReplicaSets(ns.Name).Get(ctx, pod.OwnerReferences[0].Name, metav1.GetOptions{})
	if err != nil {
		return "", "", "", err
	}
	if rs.UID != pod.OwnerReferences[0].UID || len(rs.OwnerReferences) != 1 || rs.OwnerReferences[0].Kind != "Deployment" || rs.OwnerReferences[0].UID != deployment.UID || rs.OwnerReferences[0].Name != deployment.Name || rs.Labels[installationLabel] != id || rs.OwnerReferences[0].Controller == nil || !*rs.OwnerReferences[0].Controller {
		return "", "", "", fmt.Errorf("management database controller chain changed")
	}
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Name != "postgres" || pod.Spec.Containers[0].Image != managedBackupImage {
		return "", "", "", fmt.Errorf("management database image differs from the verified installer pin")
	}
	ready := false
	for _, container := range pod.Status.ContainerStatuses {
		if container.Name == "postgres" && container.Ready && container.State.Running != nil {
			ready = true
		}
	}
	if !ready {
		return "", "", "", fmt.Errorf("management database container is not ready")
	}
	fence := sha256.Sum256([]byte(string(ns.UID) + "/" + string(deployment.UID) + "/" + string(rs.UID) + "/" + string(pod.UID)))
	return pod.Name, pod.UID, fmt.Sprintf("%x", fence), nil
}
func (c *Client) ManagedBackupDump(ctx context.Context, pod string, uid types.UID, fence string, out io.Writer) error {
	command := []string{"/bin/sh", "-c", `set -eu; export PGPASSWORD="${POSTGRES_PASSWORD:?}"; exec pg_dump --format=custom --compress=0 --no-owner --no-acl --username="${POSTGRES_USER:?}" --dbname="${POSTGRES_DB:?}"`}
	return c.managedBackupCommand(ctx, pod, uid, fence, command, out)
}
func (c *Client) ManagedBackupIdentity(ctx context.Context, pod string, uid types.UID, fence string) (string, error) {
	command := []string{"/bin/sh", "-c", `set -eu; export PGPASSWORD="${POSTGRES_PASSWORD:?}"; exec psql -X -At --set=ON_ERROR_STOP=1 --username="$POSTGRES_USER" --dbname="$POSTGRES_DB" --command="SELECT current_database() || ':' || system_identifier::text FROM pg_control_system()"`}
	output := &backupIdentityWriter{}
	if err := c.managedBackupCommand(ctx, pod, uid, fence, command, output); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}

type backupIdentityWriter struct{ strings.Builder }

func (w *backupIdentityWriter) Write(data []byte) (int, error) {
	if w.Len()+len(data) > 256 {
		return 0, fmt.Errorf("management database identity exceeded bounds")
	}
	return w.Builder.Write(data)
}
func (c *Client) managedBackupCommand(ctx context.Context, pod string, uid types.UID, fence string, command []string, out io.Writer) error {
	name, current, now, err := c.ManagedBackupPod(ctx)
	if err != nil {
		return err
	}
	if name != pod || current != uid || now != fence {
		return fmt.Errorf("management database ownership changed since backup started")
	}
	if c.execConfig == nil || c.restClient() == nil {
		return fmt.Errorf("management database exec transport is unavailable")
	}
	u := c.restClient().Post().Resource("pods").Namespace("hakopod-system").Name(pod).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "postgres", Command: command, Stdout: true, Stderr: true, TTY: false}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, u)
	if err != nil {
		return err
	}
	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: out, Stderr: io.Discard})
}
