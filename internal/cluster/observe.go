package cluster

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *Client) Observe(ctx context.Context, t Target) (Observation, error) {
	result := Observation{Revision: t.Revision, Status: "pending", Services: make([]ServiceStatus, 0, len(t.Spec.Services)), ObservedAt: time.Now().UTC()}
	if len(t.Spec.Services) == 0 {
		options := metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID), Limit: 1}
		pods, err := c.kube.CoreV1().Pods(Namespace(t.ApplicationID)).List(ctx, options)
		if err != nil {
			return result, err
		}
		deployments, err := c.kube.AppsV1().Deployments(Namespace(t.ApplicationID)).List(ctx, options)
		if err != nil {
			return result, err
		}
		if len(pods.Items) == 0 && pods.Continue == "" && len(deployments.Items) == 0 && deployments.Continue == "" {
			result.Status = "empty"
		}
		return result, nil
	}
	healthy := 0
	for _, name := range spec.Names(t.Spec) {
		svc := t.Spec.Services[name]
		if svc.Job != nil {
			var status ServiceStatus
			var err error
			if svc.Job.Schedule != nil {
				status, err = c.observeScheduledJob(ctx, t, name, svc)
			} else {
				status, err = c.observeJob(ctx, t, name, svc)
			}
			if err != nil {
				return result, err
			}
			result.Services = append(result.Services, status)
			if status.Status == "completed" || status.Status == "scheduled" || status.Status == "stopped" {
				healthy++
			}
			continue
		}
		status := ServiceStatus{Name: name, Status: "missing", Desired: serviceReplicas(svc), Image: svc.Image}
		if svc.Port > 0 {
			status.InternalAddress = fmt.Sprintf("%s:%d", name, svc.Port)
		}
		current, err := c.kube.AppsV1().Deployments(Namespace(t.ApplicationID)).Get(ctx, name, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return result, fmt.Errorf("observe %s: %w", name, err)
		}
		if err == nil {
			if err := owned(current, t); err != nil {
				return result, err
			}
			status.Ready = current.Status.ReadyReplicas
			if current.Spec.Replicas != nil {
				status.Desired = *current.Spec.Replicas
			}
			if len(current.Spec.Template.Spec.Containers) > 0 {
				status.Image = current.Spec.Template.Spec.Containers[0].Image
			}
			status.Status = "deploying"
			if readyDeployment(current) {
				ready, err := c.readyPods(ctx, t, name, current)
				if err != nil {
					return result, err
				}
				if ready && !svc.Suspended && idleDeployment(current) {
					status.Status = "sleeping"
					status.Message = "Sleeping after HTTP inactivity; the next request wakes this service"
					healthy++
				} else if ready && svc.Suspended && status.Desired == 0 {
					status.Status = "stopped"
					status.Message = "Stopped by user; configuration and volumes are retained"
					healthy++
				} else if ready {
					deliveryReady, message, err := c.serviceDeliveryHealthy(ctx, t, name, current)
					if err != nil {
						return result, err
					}
					status.Message = message
					if deliveryReady {
						status.Status = "ready"
						healthy++
					} else {
						status.Status = "failed"
					}
				} else {
					status.Message = "waiting for ready pods running the current service configuration"
				}
			} else {
				status.Message = deploymentFailure(current)
				if status.Message != "" {
					status.Status = "failed"
				} else {
					status.Message = c.podMessage(ctx, t, name)
				}
			}
		}
		if svc.Public && c.options.AppDomain != "" {
			ingress, err := c.kube.NetworkingV1().Ingresses(Namespace(t.ApplicationID)).Get(ctx, name, metav1.GetOptions{})
			if err == nil && owned(ingress, t) == nil {
				status.URL = c.serviceURL(t, name)
				status.Endpoints = map[string]string{}
				for endpoint := range svc.HTTP {
					status.Endpoints[endpoint] = strings.Replace(status.URL, c.hostname(t, name), c.endpointHostname(t, name, endpoint), 1)
				}
			}
		}
		result.Services = append(result.Services, status)
	}
	if len(result.Services) > 0 && healthy == len(result.Services) {
		result.Status = "healthy"
	} else if healthy > 0 {
		result.Status = "partial"
	}
	return result, nil
}

func (c *Client) podMessage(ctx context.Context, t Target, service string) string {
	pods, err := c.kube.CoreV1().Pods(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + serviceKey + "=" + service, Limit: 64})
	if err != nil {
		return "could not read pod diagnostics"
	}
	// Prefer the newly created, failing pod over an old ready replica retained
	// by the rolling-update strategy.
	sort.Slice(pods.Items, func(i, j int) bool {
		return pods.Items[i].CreationTimestamp.After(pods.Items[j].CreationTimestamp.Time)
	})
	for _, pod := range pods.Items {
		for _, status := range pod.Status.ContainerStatuses {
			if waiting := status.State.Waiting; waiting != nil {
				if waiting.Reason == "ImagePullBackOff" || waiting.Reason == "ErrImagePull" {
					return waiting.Reason + ": unable to pull the resolved image; verify registry access and node architecture"
				}
				if waiting.Reason == "CrashLoopBackOff" {
					return "CrashLoopBackOff: application repeatedly exits; inspect service logs and command configuration"
				}
				if waiting.Reason != "" {
					return boundedMessage(waiting.Reason + ": " + waiting.Message)
				}
			}
			if stopped := status.State.Terminated; stopped != nil {
				return fmt.Sprintf("container exited: %s (exit code %d); inspect service logs", stopped.Reason, stopped.ExitCode)
			}
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
				return boundedMessage(condition.Reason + ": " + condition.Message)
			}
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionFalse && pod.Status.Phase == corev1.PodRunning {
				return "readiness check has not passed; verify the configured port and healthcheck path"
			}
		}
	}
	return ""
}

func boundedMessage(message string) string {
	message = strings.ToValidUTF8(message, "�")
	if len(message) > 1024 {
		end := 1024
		for end > 0 && !utf8.RuneStart(message[end]) {
			end--
		}
		return message[:end] + "…"
	}
	return message
}

func (c *Client) Nodes(ctx context.Context) ([]Node, error) {
	// Pages are processed and discarded. Memory grows with at most 200 nodes,
	// not with the number of historical pods in the cluster.
	items := make([]Node, 0)
	index := make(map[string]int)
	continuation := ""
	for {
		page, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 100, Continue: continuation})
		if err != nil {
			return nil, fmt.Errorf("list Kubernetes nodes: %w", err)
		}
		for _, node := range page.Items {
			if len(items) >= 200 {
				return nil, fmt.Errorf("cluster exceeds the 200-node observation limit")
			}
			item := Node{Name: node.Name, ResourceVersion: node.ResourceVersion, ControlPlane: controlPlane(node), Unschedulable: node.Spec.Unschedulable, Architecture: node.Status.NodeInfo.Architecture, KubeletVersion: node.Status.NodeInfo.KubeletVersion, AllocatableCPU: node.Status.Allocatable.Cpu().String(), AllocatableMemory: node.Status.Allocatable.Memory().String()}
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady {
					item.Ready = condition.Status == corev1.ConditionTrue
				}
			}
			gpu := node.Status.Allocatable[corev1.ResourceName("nvidia.com/gpu")]
			item.AllocatableGPU = gpu.Value()
			index[node.Name] = len(items)
			items = append(items, item)
		}
		continuation = page.Continue
		if continuation == "" {
			break
		}
	}
	continuation, count := "", 0
	for {
		page, err := c.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 200, Continue: continuation, FieldSelector: "status.phase!=Succeeded,status.phase!=Failed"})
		if err != nil {
			return nil, fmt.Errorf("count active Kubernetes pods: %w", err)
		}
		count += len(page.Items)
		if count > 10000 {
			return nil, fmt.Errorf("cluster exceeds the 10000-active-pod observation limit")
		}
		for _, pod := range page.Items {
			if i, ok := index[pod.Spec.NodeName]; ok {
				items[i].Pods++
			}
		}
		continuation = page.Continue
		if continuation == "" {
			break
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	c.nodeMetrics(ctx, items)
	return items, nil
}

// Logs streams one current pod, chosen deterministically by newest ready pod.
// Follow streams end after the transport's 30-second limit or 8 MiB, enabling bounded reconnects;
// no stream body or log history is accumulated in the management process.
func (c *Client) Logs(ctx context.Context, namespace, service string, tail int64, follow bool) (io.ReadCloser, error) {
	if !strings.HasPrefix(namespace, "hp-") || len(namespace) != 35 || service == "" {
		return nil, fmt.Errorf("invalid application namespace or service")
	}
	if tail <= 0 {
		tail = 100
	}
	if tail > 2000 {
		tail = 2000
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if ns.Labels[managedBy] != "hakopod" || ns.Labels[ownerKey] != strings.TrimPrefix(namespace, "hp-") {
		return nil, fmt.Errorf("namespace is not owned by Hakopod")
	}
	pods, err := c.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ns.Labels[ownerKey] + "," + serviceKey + "=" + service, Limit: 64})
	if err != nil {
		return nil, err
	}
	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("service %s has no pods yet", service)
	}
	sort.Slice(pods.Items, func(i, j int) bool {
		iReady, jReady := podReady(pods.Items[i]), podReady(pods.Items[j])
		if iReady != jReady {
			return iReady
		}
		return pods.Items[i].CreationTimestamp.After(pods.Items[j].CreationTimestamp.Time)
	})
	streamCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	stream, err := c.kube.CoreV1().Pods(namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{Container: "app", TailLines: &tail, Follow: follow, Timestamps: true, LimitBytes: ptr(int64(8 << 20))}).Stream(streamCtx)
	if err != nil {
		cancel()
		return nil, err
	}
	return &logStream{Reader: io.LimitReader(stream, 8<<20), closer: stream, cancel: cancel}, nil
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

type logStream struct {
	io.Reader
	closer io.Closer
	cancel context.CancelFunc
}

func (s *logStream) Close() error { s.cancel(); return s.closer.Close() }
