package managedplatform

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

func validSchedulingPolicy(pool, runtimeClass string) bool {
	return (pool == "" || len(validation.IsValidLabelValue(pool)) == 0) && (runtimeClass == "" || pool != "" && len(validation.IsDNS1123Subdomain(runtimeClass)) == 0)
}

// exactNodeAffinity leaves scheduling and delayed volume binding to Kubernetes
// while selecting the reviewed Node name, independent of its hostname label.
func exactNodeAffinity(name string) *corev1.Affinity {
	return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchFields: []corev1.NodeSelectorRequirement{{
					Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{name},
				}},
			}},
		},
	}}
}

func applyTrustedNodePlacement(pod *corev1.PodSpec, name, pool, runtimeClass string) {
	pod.Affinity = exactNodeAffinity(name)
	if pool != "" {
		if pod.NodeSelector == nil {
			pod.NodeSelector = map[string]string{}
		}
		pod.NodeSelector["hakopod.com/pool"] = pool
		pod.Tolerations = append(pod.Tolerations, corev1.Toleration{Key: "hakopod.com/pool", Operator: corev1.TolerationOpEqual, Value: pool, Effect: corev1.TaintEffectNoSchedule})
	}
	if runtimeClass != "" {
		pod.RuntimeClassName = &runtimeClass
	}
}
