package cluster

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

func configureSharedReadWriteOnce(t Target, name string, pod *corev1.PodSpec) {
	for _, group := range spec.SharedReadWriteOnceGroups(t.Spec) {
		if !slices.Contains(group, name) {
			continue
		}
		if pod.Affinity == nil {
			pod.Affinity = &corev1.Affinity{}
		}
		// Every member, including the first pod, matches this selector. Kubernetes
		// permits the first self-affine pod to choose a node; later members and
		// replacements must join it. Existing local PV affinity survives a full stop.
		pod.Affinity.PodAffinity = &corev1.PodAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			LabelSelector: &metav1.LabelSelector{MatchLabels: labelsFor(t, ""), MatchExpressions: []metav1.LabelSelectorRequirement{{Key: serviceKey, Operator: metav1.LabelSelectorOpIn, Values: group}}},
			Namespaces:    []string{Namespace(t.ApplicationID)}, TopologyKey: corev1.LabelHostname,
		}}}
		// Restrict the first pod to nodes where the complete group fit the
		// current preflight. Self-affinity alone could strand its peers on a
		// smaller node. This remains an observation, not a capacity reservation.
		if nodes := t.sharedVolumeNodes[name]; len(nodes) > 0 {
			if pod.Affinity.NodeAffinity == nil {
				pod.Affinity.NodeAffinity = &corev1.NodeAffinity{}
			}
			if pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
				pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{}}}
			}
			selector := pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
			for i := range selector.NodeSelectorTerms {
				selector.NodeSelectorTerms[i].MatchFields = append(selector.NodeSelectorTerms[i].MatchFields, corev1.NodeSelectorRequirement{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: nodes})
			}
		}
		return
	}
}

func sharedVolumeClaims(app spec.Application, group []string) []string {
	claims := []string{}
	for _, name := range group {
		service := app.Services[name]
		if service.Volume != nil {
			claims = append(claims, name+"-data")
		}
		for _, mount := range service.Mounts {
			claim := "hakopod-volume-" + mount.Volume
			if !slices.Contains(claims, claim) {
				claims = append(claims, claim)
			}
		}
	}
	slices.Sort(claims)
	return claims
}

// sharedVolumeNodes finds common nodes without moving or modifying claims.
// Existing group pods constrain placement even when a block PV only records a
// zone. Bound local PVs and selected-node annotations also constrain stopped apps.
func (c *Client) sharedVolumeNodes(ctx context.Context, t Target, group []string, nodes []corev1.Node, pods []corev1.Pod, policy *WorkloadPolicy) ([]string, error) {
	currentNode := ""
	for _, pod := range pods {
		if pod.Namespace != Namespace(t.ApplicationID) || pod.Labels[ownerKey] != ownerID(t.ApplicationID) || pod.Labels[managedBy] != "hakopod" || !slices.Contains(group, pod.Labels[serviceKey]) || pod.Spec.NodeName == "" || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		if currentNode != "" && currentNode != pod.Spec.NodeName {
			return nil, fmt.Errorf("services %s sharing ReadWriteOnce storage are running on different nodes; stop the affected services before changing their shared storage", strings.Join(group, ", "))
		}
		currentNode = pod.Spec.NodeName
	}
	volumes := []*corev1.PersistentVolume{}
	selectedNodes := []string{}
	if t.ApplicationID != "" {
		for _, name := range sharedVolumeClaims(t.Spec, group) {
			claim, err := c.kube.CoreV1().PersistentVolumeClaims(Namespace(t.ApplicationID)).Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if err := owned(claim, t); err != nil {
				return nil, err
			}
			if claim.Spec.VolumeName == "" {
				if selected := claim.Annotations["volume.kubernetes.io/selected-node"]; selected != "" {
					selectedNodes = append(selectedNodes, selected)
				}
				continue
			}
			volume, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			volumes = append(volumes, volume)
		}
	}
	result := []string{}
	for _, node := range nodes {
		if node.Labels[corev1.LabelHostname] == "" || currentNode != "" && currentNode != node.Name || policy != nil && (policy.NodeName != node.Name || policy.Pool != "" && node.Labels["hakopod.com/pool"] != policy.Pool) {
			continue
		}
		fits := true
		for _, name := range group {
			service := t.Spec.Services[name]
			if nodeUnavailable(node, policy, service.GPU != nil) != "" || service.NodeName != "" && service.NodeName != node.Name || service.Architecture != "" && service.Architecture != node.Labels[corev1.LabelArchStable] {
				fits = false
			}
		}
		for _, selected := range selectedNodes {
			fits = fits && selected == node.Name
		}
		for _, volume := range volumes {
			fits = fits && volumeFitsNode(volume, &node)
		}
		if fits {
			result = append(result, node.Name)
		}
	}
	slices.Sort(result)
	return result, nil
}

func (c *Client) validateSharedReadWriteOncePlacement(parent context.Context, t Target) error {
	groups := spec.SharedReadWriteOnceGroups(t.Spec)
	if len(groups) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	policy, err := c.workloadPolicy(ctx, t)
	if err != nil {
		return err
	}
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 201})
	if err != nil {
		return err
	}
	if nodes.Continue != "" || len(nodes.Items) > 200 {
		return fmt.Errorf("shared volume placement exceeds 200 nodes")
	}
	pods := &corev1.PodList{}
	if t.ApplicationID != "" {
		pods, err = c.kube.CoreV1().Pods(Namespace(t.ApplicationID)).List(ctx, metav1.ListOptions{LabelSelector: labels.Set(labelsFor(t, "")).String(), Limit: 1001})
		if err != nil {
			return err
		}
		if pods.Continue != "" || len(pods.Items) > 1000 {
			return fmt.Errorf("shared volume placement exceeds 1000 application pods")
		}
	}
	for _, group := range groups {
		available, err := c.sharedVolumeNodes(ctx, t, group, nodes.Items, pods.Items, policy)
		if err != nil {
			return err
		}
		if len(available) == 0 {
			return fmt.Errorf("services %s sharing ReadWriteOnce storage have no common available node; restore their storage node or migrate the retained data before moving services", strings.Join(group, ", "))
		}
	}
	return nil
}
