package managedplatform

import corev1 "k8s.io/api/core/v1"

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
