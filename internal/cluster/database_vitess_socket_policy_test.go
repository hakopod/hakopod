package cluster

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func vitessSocketPolicyPod() corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
			"dev.gvisor.spec.mount.rundir.share":   "pod",
			"dev.gvisor.spec.mount.rundir.type":    "tmpfs",
			"dev.gvisor.spec.mount.rundir.options": "rw,rprivate,size=16777216",
		}},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{Name: "rundir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: resource.NewQuantity(16*1024*1024, resource.BinarySI)}}}},
			Containers: []corev1.Container{
				{Name: "mysqld", ReadinessProbe: vitessTabletReadinessProbe("mysqld"), VolumeMounts: []corev1.VolumeMount{{Name: "rundir", MountPath: "/vt/socket"}}},
				{Name: "vttablet", ReadinessProbe: vitessTabletReadinessProbe("vttablet"), VolumeMounts: []corev1.VolumeMount{{Name: "rundir", MountPath: "/vt/socket"}}},
			},
		},
	}
}

func TestVitessTabletSocketMatches(t *testing.T) {
	if !vitessTabletSocketMatches(vitessSocketPolicyPod()) {
		t.Fatal("accepted socket policy did not match")
	}
	for name, mutate := range map[string]func(*corev1.Pod){
		"missing-volume":          func(p *corev1.Pod) { p.Spec.Volumes = nil },
		"unbounded":               func(p *corev1.Pod) { p.Spec.Volumes[0].EmptyDir.SizeLimit = nil },
		"disk":                    func(p *corev1.Pod) { p.Spec.Volumes[0].EmptyDir.Medium = corev1.StorageMediumDefault },
		"subpath":                 func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].SubPath = "socket" },
		"readonly":                func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].ReadOnly = true },
		"wrong-volume":            func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].Name = "vt-root" },
		"wrong-path":              func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].MountPath = "/tmp/socket" },
		"missing-container-mount": func(p *corev1.Pod) { p.Spec.Containers[1].VolumeMounts = nil },
		"duplicate-mount": func(p *corev1.Pod) {
			p.Spec.Containers[1].VolumeMounts = append(p.Spec.Containers[1].VolumeMounts, p.Spec.Containers[1].VolumeMounts[0])
		},
		"duplicate-container-name": func(p *corev1.Pod) { p.Spec.Containers[1].Name = "mysqld" },
		"missing-readiness":        func(p *corev1.Pod) { p.Spec.Containers[0].ReadinessProbe = nil },
		"wrong-readiness-command":  func(p *corev1.Pod) { p.Spec.Containers[1].ReadinessProbe.Exec.Command[2] = "true" },
		"wrong-readiness-timeout":  func(p *corev1.Pod) { p.Spec.Containers[1].ReadinessProbe.TimeoutSeconds = 1 },
		"missing-annotation":       func(p *corev1.Pod) { delete(p.Annotations, "dev.gvisor.spec.mount.rundir.share") },
		"wrong-annotation":         func(p *corev1.Pod) { p.Annotations["dev.gvisor.spec.mount.rundir.options"] = "rw" },
	} {
		t.Run(name, func(t *testing.T) {
			pod := vitessSocketPolicyPod()
			mutate(&pod)
			if vitessTabletSocketMatches(pod) {
				t.Fatal("accepted unsafe tablet socket policy")
			}
		})
	}
}
