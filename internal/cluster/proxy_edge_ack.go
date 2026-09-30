package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

const edgeMaximumIngressPods = 8

// An acknowledgement uses a process variable read from the running worker.
// Seeing the generated file alone cannot prove that HAProxy accepted a reload.
func (c *Client) waitEdgeApplied(ctx context.Context, wanted *corev1.ConfigMap, policy EdgePolicy) error {
	if c.edgeAck != nil {
		return c.edgeAck(ctx, policy)
	}
	if c.execConfig == nil || c.restClient() == nil {
		return fmt.Errorf("%w: access to the owned ingress runtime is required", ErrEdgeNotAcknowledged)
	}
	wait, done := context.WithTimeout(ctx, 30*time.Second)
	defer done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var last error
	for {
		current, err := c.proxyConfigMap(wait)
		if err == nil {
			var observed EdgePolicy
			observed, err = readEdgePolicy(current)
			if err == nil && (edgePolicyHash(observed) != edgePolicyHash(policy) || current.Annotations["hakopod.io/proxy-revision"] != wanted.Annotations["hakopod.io/proxy-revision"]) {
				return ErrProxyConflict
			}
		}
		if err == nil {
			err = c.edgeRuntimeApplied(wait, policy)
		}
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrProxyConflict) || errors.Is(err, ErrEdgeUnsupported) {
			return err
		}
		if wait.Err() == nil || last == nil {
			last = err
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("%w: %v", ErrEdgeNotAcknowledged, last)
		case <-ticker.C:
		}
	}
}

func (c *Client) edgePreflight(ctx context.Context, policy EdgePolicy) error {
	if c.edgeAck != nil {
		return nil
	}
	if c.execConfig == nil || c.restClient() == nil {
		return fmt.Errorf("%w: access to the owned ingress runtime is required", ErrEdgeUnsupported)
	}
	_, err := c.edgeIngressPods(ctx, policy)
	return err
}

func (c *Client) edgeRuntimeApplied(ctx context.Context, policy EdgePolicy) error {
	pods, err := c.edgeIngressPods(ctx, policy)
	if err != nil {
		return err
	}
	for _, pod := range pods {
		state, err := c.readEdgeRuntime(ctx, pod)
		if err != nil {
			return err
		}
		if err := validateEdgeRuntime(state, policy); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) edgeIngressPods(ctx context.Context, policy EdgePolicy) ([]corev1.Pod, error) {
	deployment, err := c.kube.AppsV1().Deployments(c.options.ProxyNamespace).Get(ctx, c.options.ProxyConfigMap, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspect owned ingress deployment: %w", err)
	}
	if !c.ownsProxy(deployment) {
		return nil, fmt.Errorf("%w: ingress deployment is not owned by the configured release", ErrEdgeUnsupported)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas < 1 || *deployment.Spec.Replicas > edgeMaximumIngressPods {
		return nil, fmt.Errorf("%w: edge acknowledgement supports 1–%d ingress replicas", ErrEdgeUnsupported, edgeMaximumIngressPods)
	}
	if deployment.Status.ObservedGeneration < deployment.Generation || deployment.Status.ReadyReplicas != *deployment.Spec.Replicas || deployment.Status.UpdatedReplicas != *deployment.Spec.Replicas || deployment.Status.Replicas != *deployment.Spec.Replicas {
		return nil, fmt.Errorf("waiting for the owned ingress deployment to become ready")
	}
	service, err := c.kube.CoreV1().Services(c.options.ProxyNamespace).Get(ctx, c.options.ProxyConfigMap, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspect owned ingress Service: %w", err)
	}
	if !c.ownsProxy(service) || service.Spec.Selector["app.kubernetes.io/name"] != "kubernetes-ingress" || service.Spec.Selector["app.kubernetes.io/instance"] != c.options.ProxyRelease {
		return nil, fmt.Errorf("%w: ingress Service is not owned by the configured release", ErrEdgeUnsupported)
	}
	if policy.Enabled && len(policy.Rules) > 0 && !deployment.Spec.Template.Spec.HostNetwork && (service.Spec.Type == corev1.ServiceTypeNodePort || service.Spec.Type == corev1.ServiceTypeLoadBalancer) && service.Spec.ExternalTrafficPolicy != corev1.ServiceExternalTrafficPolicyLocal {
		return nil, fmt.Errorf("%w: ingress Service must preserve client addresses with externalTrafficPolicy Local", ErrEdgeUnsupported)
	}
	list, err := c.kube.CoreV1().Pods(c.options.ProxyNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=kubernetes-ingress,app.kubernetes.io/instance=" + c.options.ProxyRelease, Limit: edgeMaximumIngressPods + 1})
	if err != nil {
		return nil, err
	}
	if list.Continue != "" || len(list.Items) > edgeMaximumIngressPods {
		return nil, fmt.Errorf("waiting for a stable ingress pod inventory within the acknowledgement bound")
	}
	ready := []corev1.Pod{}
	for _, pod := range list.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		if pod.Status.Phase != corev1.PodRunning || !edgePodReady(pod) {
			return nil, fmt.Errorf("waiting for all ingress pods to be ready")
		}
		if err := c.edgePodOwned(ctx, pod, deployment.UID); err != nil {
			return nil, err
		}
		found := false
		for _, container := range pod.Spec.Containers {
			if container.Name != "kubernetes-ingress-controller" {
				continue
			}
			found = true
			for i, argument := range container.Args {
				if !strings.HasPrefix(argument, "--disable-config-snippets") {
					continue
				}
				value := strings.TrimPrefix(argument, "--disable-config-snippets=")
				if argument == "--disable-config-snippets" && i+1 < len(container.Args) {
					value = container.Args[i+1]
				}
				if value != "" {
					return nil, fmt.Errorf("%w: the controller disables the snippets required by edge policy", ErrEdgeUnsupported)
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: the owned ingress controller container is missing", ErrEdgeUnsupported)
		}
		ready = append(ready, pod)
	}
	if len(ready) != int(*deployment.Spec.Replicas) {
		return nil, fmt.Errorf("waiting for every desired ingress replica to acknowledge edge policy")
	}
	return ready, nil
}

func edgePodReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (c *Client) edgePodOwned(ctx context.Context, pod corev1.Pod, deploymentUID types.UID) error {
	owner := metav1.GetControllerOf(&pod)
	if owner == nil || owner.Kind != "ReplicaSet" || owner.APIVersion != "apps/v1" {
		return fmt.Errorf("%w: ingress pod is not controlled by the owned deployment", ErrEdgeUnsupported)
	}
	set, err := c.kube.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("inspect ingress ReplicaSet ownership: %w", err)
	}
	controller := metav1.GetControllerOf(set)
	if set.UID != owner.UID || controller == nil || controller.Kind != "Deployment" || controller.APIVersion != "apps/v1" || controller.UID != deploymentUID {
		return fmt.Errorf("%w: ingress pod does not belong to the configured deployment", ErrEdgeUnsupported)
	}
	return nil
}

type edgeRuntime struct {
	revision  string
	frontends map[string]string
	active    []string
}

func (c *Client) readEdgeRuntime(ctx context.Context, pod corev1.Pod) (edgeRuntime, error) {
	// Return only the owned rule block, its one process variable, and the names
	// of active standard HTTP listeners. Never emit certificates or request logs.
	script := `set -eu
printf 'REVISION\n'
printf 'get var proc.hakopod_edge_revision\n' | socat -t 2 - UNIX-CONNECT:/var/run/haproxy-runtime-api.sock
printf 'CONFIG\n'
awk '
/^[^ \t]/ { front=($1=="frontend" && ($2=="http" || $2=="https")); name=$2; show=0 }
{ line=$0; sub(/^[ \t]+/, "", line) }
front && line=="# BEGIN hakopod edge" { print "FRONTEND " name; show=1 }
show { print line }
line=="# END hakopod edge" { show=0 }
' /etc/haproxy/haproxy.cfg
printf 'ACTIVE\n'
printf 'show stat\n' | socat -t 2 - UNIX-CONNECT:/var/run/haproxy-runtime-api.sock | awk -F, '($1=="http" || $1=="https") && $2=="FRONTEND" && $18=="OPEN" {print $1}'
printf 'END\n'
`
	command := []string{"sh", "-c", script, "hakopod-edge-probe"}
	request := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "kubernetes-ingress-controller", Command: command, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, request.URL())
	if err != nil {
		return edgeRuntime{}, err
	}
	output := &tcpBoundedWriter{limit: 512 << 10}
	probe, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	if err = executor.StreamWithContext(probe, remotecommand.StreamOptions{Stdout: output, Stderr: io.Discard}); err != nil {
		return edgeRuntime{}, fmt.Errorf("inspect ingress edge runtime: %w", err)
	}
	return parseEdgeRuntime(output.String())
}

func parseEdgeRuntime(value string) (edgeRuntime, error) {
	state := edgeRuntime{frontends: map[string]string{}}
	if !strings.HasPrefix(value, "REVISION\n") || !strings.HasSuffix(value, "END\n") {
		return state, fmt.Errorf("incomplete edge runtime acknowledgement")
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "REVISION\n"), "CONFIG\n", 2)
	if len(parts) != 2 {
		return state, fmt.Errorf("incomplete edge runtime acknowledgement")
	}
	prefix := edgeRuntimeVariable + ": type=str value=<"
	revision := strings.TrimSpace(parts[0])
	if !strings.HasPrefix(revision, prefix) || !strings.HasSuffix(revision, ">") {
		return state, fmt.Errorf("active HAProxy worker has not loaded an edge revision")
	}
	state.revision = strings.TrimSuffix(strings.TrimPrefix(revision, prefix), ">")
	if len(state.revision) != 64 || strings.ContainsAny(state.revision, "\n\r ") {
		return state, fmt.Errorf("invalid edge revision acknowledgement")
	}
	sections := strings.SplitN(parts[1], "ACTIVE\n", 2)
	if len(sections) != 2 {
		return state, fmt.Errorf("incomplete edge listener acknowledgement")
	}
	for _, block := range strings.Split(sections[0], "FRONTEND ")[1:] {
		name, content, found := strings.Cut(block, "\n")
		if !found || !slices.Contains([]string{"http", "https"}, name) || state.frontends[name] != "" {
			return state, fmt.Errorf("invalid edge frontend acknowledgement")
		}
		state.frontends[name] = strings.TrimSpace(content)
	}
	state.active = strings.Fields(strings.TrimSuffix(sections[1], "END\n"))
	return state, nil
}

func validateEdgeRuntime(state edgeRuntime, policy EdgePolicy) error {
	if state.revision != edgePolicyHash(policy) {
		return fmt.Errorf("waiting for the active HAProxy worker to load the reviewed edge revision")
	}
	if len(state.active) < 1 || len(state.active) > 2 {
		return fmt.Errorf("edge acknowledgement requires active HTTP or HTTPS listeners")
	}
	wanted := edgeFrontendBlock(policy)
	seen := map[string]bool{}
	for _, name := range state.active {
		if !slices.Contains([]string{"http", "https"}, name) || seen[name] || state.frontends[name] != wanted {
			return fmt.Errorf("generated edge rules do not match the reviewed policy on every active listener")
		}
		seen[name] = true
	}
	return nil
}
