package cluster

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/managedplatform"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ManagedPlatformNodeReservation struct {
	UID        string
	Capacity   managedplatform.Capacity
	Namespaces map[string]managedplatform.CapacityNamespaceOwnership
}

// DatabaseNodeReservation covers all grants sharing this node. Scopes identify
// managed database namespaces already included in that reserved envelope.
type DatabaseNodeReservation struct {
	UID                       string
	Capacity                  database.Capacity
	Scopes                    []string
	ManagedPlatformNamespaces map[string]managedplatform.CapacityNamespaceOwnership
}

func validManagedPlatformLabels(labels map[string]string, owned managedplatform.CapacityNamespaceOwnership) bool {
	return labels[managedBy] == "hakopod" && labels["hakopod.io/managed-platform-id"] == owned.PlatformID && labels["hakopod.io/managed-platform"] == owned.PlatformName
}

func capacityContainersMatch(actual, planned []corev1.Container) bool {
	if len(actual) != len(planned) {
		return false
	}
	for i := range actual {
		if actual[i].Name != planned[i].Name || actual[i].Image != planned[i].Image || actual[i].Resources.Requests.Cpu().MilliValue() != planned[i].Resources.Requests.Cpu().MilliValue() || actual[i].Resources.Requests.Memory().Value() != planned[i].Resources.Requests.Memory().Value() {
			return false
		}
	}
	return true
}

func capacityTemplateMatches(pod corev1.Pod, planned corev1.PodTemplateSpec) bool {
	if pod.Spec.NodeName == "" || planned.Spec.NodeName != "" && pod.Spec.NodeName != planned.Spec.NodeName || !capacityContainersMatch(pod.Spec.Containers, planned.Spec.Containers) || !capacityContainersMatch(pod.Spec.InitContainers, planned.Spec.InitContainers) {
		return false
	}
	for key, value := range planned.Spec.NodeSelector {
		if pod.Spec.NodeSelector[key] != value {
			return false
		}
	}
	for key, value := range planned.Labels {
		if pod.Labels[key] != value {
			return false
		}
	}
	return true
}

func capacityTemplatesMatch(actual, planned corev1.PodTemplateSpec) bool {
	if actual.Spec.NodeName != planned.Spec.NodeName || !capacityContainersMatch(actual.Spec.Containers, planned.Spec.Containers) || !capacityContainersMatch(actual.Spec.InitContainers, planned.Spec.InitContainers) {
		return false
	}
	for key, value := range planned.Labels {
		if actual.Labels[key] != value {
			return false
		}
	}
	return true
}

func workloadRequestsMatch(spec corev1.PodSpec, expected managedplatform.Capacity) bool {
	cpu, memory := podRequests(spec)
	return cpu == expected.CPUMilli && memory+managedplatform.PodMemoryOverheadBytes == expected.MemoryBytes
}

func (c *Client) managedPlatformPodReservation(ctx context.Context, pod corev1.Pod, ns *corev1.Namespace, owned managedplatform.CapacityNamespaceOwnership) (string, managedplatform.Capacity, bool, error) {
	if ns == nil || owned.UID == "" || string(ns.UID) != owned.UID || ns.Name != "managed-platform-"+owned.PlatformID || ns.Labels[managedBy] != "hakopod" || ns.Labels["hakopod.io/managed-platform-id"] != owned.PlatformID || !validManagedPlatformLabels(pod.Labels, owned) || len(pod.OwnerReferences) != 1 {
		return "", managedplatform.Capacity{}, false, nil
	}
	owner := pod.OwnerReferences[0]
	if owner.UID == "" {
		return "", managedplatform.Capacity{}, false, nil
	}
	reservation := func(key, uid string) (managedplatform.Capacity, bool) {
		workload, ok := owned.Workloads[key]
		if !ok || workload.UID != uid {
			return managedplatform.Capacity{}, false
		}
		capacity, ok := workload.Nodes[pod.Spec.NodeName]
		return capacity, ok
	}
	switch owner.APIVersion + "/" + owner.Kind {
	case "apps/v1/StatefulSet":
		item, err := c.kube.AppsV1().StatefulSets(ns.Name).Get(ctx, owner.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return "", managedplatform.Capacity{}, false, nil
		}
		if err != nil {
			return "", managedplatform.Capacity{}, false, err
		}
		key := "statefulset." + item.Name
		expected, ok := reservation(key, string(item.UID))
		if !ok || item.UID != owner.UID || !validManagedPlatformLabels(item.Labels, owned) || item.Spec.Replicas == nil || *item.Spec.Replicas != 1 || pod.Name != item.Name+"-0" || !capacityTemplateMatches(pod, item.Spec.Template) || !workloadRequestsMatch(item.Spec.Template.Spec, expected) {
			return "", managedplatform.Capacity{}, false, nil
		}
		return key, expected, true, nil
	case "apps/v1/ReplicaSet":
		replica, err := c.kube.AppsV1().ReplicaSets(ns.Name).Get(ctx, owner.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return "", managedplatform.Capacity{}, false, nil
		}
		if err != nil {
			return "", managedplatform.Capacity{}, false, err
		}
		if replica.UID != owner.UID || !validManagedPlatformLabels(replica.Labels, owned) || len(replica.OwnerReferences) != 1 {
			return "", managedplatform.Capacity{}, false, nil
		}
		deploymentOwner := replica.OwnerReferences[0]
		if deploymentOwner.APIVersion != "apps/v1" || deploymentOwner.Kind != "Deployment" || deploymentOwner.UID == "" {
			return "", managedplatform.Capacity{}, false, nil
		}
		deployment, err := c.kube.AppsV1().Deployments(ns.Name).Get(ctx, deploymentOwner.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return "", managedplatform.Capacity{}, false, nil
		}
		if err != nil {
			return "", managedplatform.Capacity{}, false, err
		}
		key := "deployment." + deployment.Name
		expected, ok := reservation(key, string(deployment.UID))
		if !ok || deployment.UID != deploymentOwner.UID || !validManagedPlatformLabels(deployment.Labels, owned) || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 || deployment.Spec.Strategy.Type != "Recreate" || replica.Spec.Replicas == nil || *replica.Spec.Replicas > 1 || !capacityTemplatesMatch(replica.Spec.Template, deployment.Spec.Template) || !capacityTemplateMatches(pod, replica.Spec.Template) || !workloadRequestsMatch(replica.Spec.Template.Spec, expected) {
			return "", managedplatform.Capacity{}, false, nil
		}
		return key, expected, true, nil
	default:
		return "", managedplatform.Capacity{}, false, nil
	}
}

func addManagedPlatformExcess(cpu, memory *int64, usage, envelope map[string]managedplatform.Capacity) {
	for key, actual := range usage {
		expected := envelope[key]
		*cpu += max(int64(0), actual.CPUMilli-expected.CPUMilli)
		*memory += max(int64(0), actual.MemoryBytes-expected.MemoryBytes)
	}
}

func (c *Client) checkDatabaseNodeReservation(ctx context.Context, node corev1.Node, r DatabaseNodeReservation) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if r.UID == "" || string(node.UID) != r.UID || r.Capacity.CPUMilli < 1 || r.Capacity.MemoryBytes < 1 || len(r.Scopes) < 1 || len(r.Scopes) > 64 {
		return fmt.Errorf("database node identity or capacity reservation is invalid")
	}
	// Allocatable already excludes kube/system reservations when configured. Keep
	// additional headroom for bounded probes and host processes on every node.
	cpu := r.Capacity.CPUMilli + 250
	memory := r.Capacity.MemoryBytes + (256 << 20)
	pods, err := c.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name, Limit: 1001})
	if err != nil || pods.Continue != "" || len(pods.Items) > 1000 {
		return fmt.Errorf("database node workload inventory is unavailable or exceeds its bound")
	}
	covered := map[string]bool{}
	platformNamespaces := map[string]*corev1.Namespace{}
	managedUsage := map[string]managedplatform.Capacity{}
	managedEnvelope := map[string]managedplatform.Capacity{}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != node.Name || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		inGrant, seen := covered[pod.Namespace]
		if strings.HasPrefix(pod.Namespace, "managed-platform-") {
			owned, ok := r.ManagedPlatformNamespaces[pod.Namespace]
			if ok {
				ns, loaded := platformNamespaces[pod.Namespace]
				if !loaded {
					var e error
					ns, e = c.kube.CoreV1().Namespaces().Get(ctx, pod.Namespace, metav1.GetOptions{})
					if e != nil {
						return fmt.Errorf("managed platform namespace identity is unavailable")
					}
					platformNamespaces[pod.Namespace] = ns
				}
				var e error
				var workload string
				var envelope managedplatform.Capacity
				workload, envelope, inGrant, e = c.managedPlatformPodReservation(ctx, pod, ns, owned)
				if e != nil {
					return fmt.Errorf("managed platform workload identity is unavailable")
				}
				if inGrant {
					key := pod.Namespace + "\x00" + workload
					usedCPU, usedMemory := podRequests(pod.Spec)
					managedUsage[key] = managedUsage[key].Add(managedplatform.Capacity{CPUMilli: usedCPU + pod.Spec.Overhead.Cpu().MilliValue(), MemoryBytes: usedMemory + pod.Spec.Overhead.Memory().Value()})
					managedEnvelope[key] = envelope
				}
			} else {
				inGrant = false
			}
		} else if !seen {
			if strings.HasPrefix(pod.Namespace, "hdb-") {
				ns, e := c.kube.CoreV1().Namespaces().Get(ctx, pod.Namespace, metav1.GetOptions{})
				if e != nil {
					return fmt.Errorf("database namespace identity is unavailable")
				}
				inGrant = ns.Labels[managedBy] == "hakopod" && ns.Labels[databaseOwner] != "" && ns.Name == DatabaseNamespace(ns.Labels[databaseOwner]) && slices.Contains(r.Scopes, ns.Labels["hakopod.io/project"]+"/"+ns.Labels["hakopod.io/environment"])
			}
			covered[pod.Namespace] = inGrant
		}
		if inGrant {
			continue
		}
		usedCPU, usedMemory := podRequests(pod.Spec)
		cpu += usedCPU + pod.Spec.Overhead.Cpu().MilliValue()
		memory += usedMemory + pod.Spec.Overhead.Memory().Value()
	}
	addManagedPlatformExcess(&cpu, &memory, managedUsage, managedEnvelope)
	if cpu > node.Status.Allocatable.Cpu().MilliValue() || memory > node.Status.Allocatable.Memory().Value() {
		return fmt.Errorf("database grants and existing workloads exceed node capacity including system headroom")
	}
	return nil
}

func (c *Client) CheckDatabaseNodeReservations(ctx context.Context, reservations map[string]DatabaseNodeReservation) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if len(reservations) > database.MaxMembers {
		return fmt.Errorf("database reservation inventory exceeds its bound")
	}
	for name, reservation := range reservations {
		node, err := c.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("database reservation node is unavailable")
		}
		if err = c.checkDatabaseNodeReservation(ctx, *node, reservation); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) CheckManagedPlatformNodeReservations(ctx context.Context, reservations map[string]ManagedPlatformNodeReservation) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if len(reservations) < 1 || len(reservations) > 48 {
		return fmt.Errorf("managed platform node reservation inventory is invalid")
	}
	for name, reservation := range reservations {
		node, err := c.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil || reservation.UID == "" || string(node.UID) != reservation.UID || reservation.Capacity.CPUMilli < 1 || reservation.Capacity.MemoryBytes < 1 || len(reservation.Namespaces) > managedplatform.MaxCapacityReservations {
			return fmt.Errorf("managed platform reservation node is unavailable or invalid")
		}
		cpu := reservation.Capacity.CPUMilli + 250
		memory := reservation.Capacity.MemoryBytes + (256 << 20)
		pods, listErr := c.kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + name, Limit: 1001})
		if listErr != nil || pods.Continue != "" || len(pods.Items) > 1000 {
			return fmt.Errorf("managed platform node workload inventory is unavailable or exceeds its bound")
		}
		platformNamespaces := map[string]*corev1.Namespace{}
		managedUsage := map[string]managedplatform.Capacity{}
		managedEnvelope := map[string]managedplatform.Capacity{}
		for _, pod := range pods.Items {
			if pod.Spec.NodeName != name || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
				continue
			}
			inGrant := false
			if strings.HasPrefix(pod.Namespace, "managed-platform-") {
				owned, ok := reservation.Namespaces[pod.Namespace]
				ns, loaded := platformNamespaces[pod.Namespace]
				if ok && !loaded {
					var namespaceErr error
					ns, namespaceErr = c.kube.CoreV1().Namespaces().Get(ctx, pod.Namespace, metav1.GetOptions{})
					if namespaceErr != nil {
						return fmt.Errorf("managed platform namespace identity is unavailable")
					}
					platformNamespaces[pod.Namespace] = ns
				}
				if ok {
					var ownershipErr error
					var workload string
					var envelope managedplatform.Capacity
					workload, envelope, inGrant, ownershipErr = c.managedPlatformPodReservation(ctx, pod, ns, owned)
					if ownershipErr != nil {
						return fmt.Errorf("managed platform workload identity is unavailable")
					}
					if inGrant {
						key := pod.Namespace + "\x00" + workload
						usedCPU, usedMemory := podRequests(pod.Spec)
						managedUsage[key] = managedUsage[key].Add(managedplatform.Capacity{CPUMilli: usedCPU + pod.Spec.Overhead.Cpu().MilliValue(), MemoryBytes: usedMemory + pod.Spec.Overhead.Memory().Value()})
						managedEnvelope[key] = envelope
					}
				}
			}
			if inGrant {
				continue
			}
			usedCPU, usedMemory := podRequests(pod.Spec)
			cpu += usedCPU + pod.Spec.Overhead.Cpu().MilliValue()
			memory += usedMemory + pod.Spec.Overhead.Memory().Value()
		}
		addManagedPlatformExcess(&cpu, &memory, managedUsage, managedEnvelope)
		if cpu > node.Status.Allocatable.Cpu().MilliValue() || memory > node.Status.Allocatable.Memory().Value() {
			return fmt.Errorf("managed platform grants and existing workloads exceed node capacity including system headroom")
		}
	}
	return nil
}
