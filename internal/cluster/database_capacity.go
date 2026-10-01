package cluster

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DatabaseNodeReservation covers all grants sharing this node. Scopes identify
// managed database namespaces already included in that reserved envelope.
type DatabaseNodeReservation struct {
	UID      string
	Capacity database.Capacity
	Scopes   []string
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
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != node.Name || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		inGrant, seen := covered[pod.Namespace]
		if !seen {
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
	if cpu > node.Status.Allocatable.Cpu().MilliValue() || memory > node.Status.Allocatable.Memory().Value() {
		return fmt.Errorf("database grants and existing workloads exceed node capacity including system headroom")
	}
	return nil
}

func (c *Client) CheckDatabaseNodeReservations(ctx context.Context, reservations map[string]DatabaseNodeReservation) error {
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
