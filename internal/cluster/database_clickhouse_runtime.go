package cluster

import (
	"context"
	"fmt"
	"slices"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const clickhouseRuntimeClass = "hakopod-clickhouse"
const clickhouseRuntimeLabel = "hakopod.com.node-restriction.kubernetes.io/clickhouse-runtime"
const clickhouseRuntimeProfile = "systrap-no-patching-v1"

// ClickHouse checks its loaded executable. Systrap's syscall patching changes
// those bytes. Keep integrity checking enabled and select a compatibility
// profile; never allow tenant annotations to override arbitrary runsc flags.
func (c *Client) clickhouseRuntime(ctx context.Context, d database.Resource, policy *DatabasePolicy) (string, error) {
	if !c.options.ClickHouseSandbox && policy == nil {
		return "", nil
	}
	r, err := c.kube.NodeV1().RuntimeClasses().Get(ctx, clickhouseRuntimeClass, metav1.GetOptions{})
	if err != nil || r.Handler != clickhouseRuntimeClass || r.Labels[managedBy] != "hakopod" || r.Annotations[clickhouseRuntimeLabel] != clickhouseRuntimeProfile || r.Scheduling == nil || r.Scheduling.NodeSelector[clickhouseRuntimeLabel] != clickhouseRuntimeProfile {
		return "", fmt.Errorf("the verified ClickHouse sandbox profile is unavailable")
	}
	names := d.Spec.Placement.NodeNames
	if len(names) == 0 && policy != nil {
		names = policy.nodes()
	}
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 257})
	if err != nil || len(nodes.Items) > 256 || nodes.Continue != "" {
		return "", fmt.Errorf("ClickHouse sandbox node inventory is unavailable")
	}
	ready, domains := map[string]bool{}, map[string]bool{}
	for _, n := range nodes.Items {
		if len(names) > 0 && !slices.Contains(names, n.Name) {
			continue
		}
		if n.Spec.Unschedulable || n.DeletionTimestamp != nil || n.Labels[clickhouseRuntimeLabel] != clickhouseRuntimeProfile {
			continue
		}
		for _, condition := range n.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				ready[n.Name] = true
				if domain := n.Labels[databaseTopologyKey(d.Spec)]; domain != "" {
					domains[domain] = true
				}
			}
		}
	}
	for _, name := range names {
		if !ready[name] {
			return "", fmt.Errorf("a selected worker has no verified ClickHouse sandbox")
		}
	}
	if len(ready) == 0 || (d.Spec.Placement.Spread != "" && len(domains) < d.Spec.PlacementDomains()) {
		return "", fmt.Errorf("the ClickHouse sandbox has insufficient ready placement domains")
	}
	return clickhouseRuntimeClass, nil
}

func applyClickHouseRuntime(object *unstructured.Unstructured, runtime string) {
	if runtime == "" {
		return
	}
	templates, _, _ := unstructured.NestedSlice(object.Object, "spec", "templates", "podTemplates")
	for _, raw := range templates {
		pod := raw.(map[string]any)["spec"].(map[string]any)
		pod["runtimeClassName"] = runtime
		selector, _ := pod["nodeSelector"].(map[string]any)
		if selector == nil {
			selector = map[string]any{}
		}
		selector[clickhouseRuntimeLabel] = clickhouseRuntimeProfile
		pod["nodeSelector"] = selector
	}
	_ = unstructured.SetNestedSlice(object.Object, templates, "spec", "templates", "podTemplates")
}

func clickhousePodRuntimeMatches(pod corev1.Pod, runtime string) bool {
	if runtime == "" {
		return pod.Spec.RuntimeClassName == nil
	}
	return pod.Spec.RuntimeClassName != nil && *pod.Spec.RuntimeClassName == runtime && pod.Spec.NodeSelector[clickhouseRuntimeLabel] == clickhouseRuntimeProfile
}
