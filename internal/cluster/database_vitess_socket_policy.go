package cluster

import (
	"reflect"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	vitessSocketVolumeName = "rundir"
	vitessSocketMountPath  = "/vt/socket"
)

var vitessSocketAnnotations = map[string]string{
	"dev.gvisor.spec.mount.rundir.share":   "pod",
	"dev.gvisor.spec.mount.rundir.type":    "tmpfs",
	"dev.gvisor.spec.mount.rundir.options": "rw,rprivate,size=16777216",
}

func vitessTabletReadinessProbe(container string) *corev1.Probe {
	switch container {
	case "vttablet":
		return &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{
				"bash", "-ceu", "curl --fail --silent --show-error --max-time 2 http://127.0.0.1:15000/healthz >/dev/null; test -S /vt/socket/mysql.sock; test -S /vt/socket/mysqlctl.sock",
			}}},
			TimeoutSeconds:   3,
			PeriodSeconds:    10,
			SuccessThreshold: 1,
			FailureThreshold: 3,
		}
	case "mysqld":
		return &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{
				"bash", "-ceu", "exec 3<>/dev/tcp/127.0.0.1/3306; exec 3>&-; test -S /vt/socket/mysql.sock; test -S /vt/socket/mysqlctl.sock",
			}}},
			TimeoutSeconds:   1,
			PeriodSeconds:    2,
			SuccessThreshold: 1,
			FailureThreshold: 3,
		}
	default:
		return nil
	}
}

func vitessTabletSocketMatches(pod corev1.Pod) bool {
	for name, value := range vitessSocketAnnotations {
		if pod.Annotations[name] != value {
			return false
		}
	}
	volumes := 0
	for _, volume := range pod.Spec.Volumes {
		if volume.Name != vitessSocketVolumeName {
			continue
		}
		volumes++
		if volume.EmptyDir == nil || volume.EmptyDir.Medium != corev1.StorageMediumMemory || volume.EmptyDir.SizeLimit == nil || volume.EmptyDir.SizeLimit.Cmp(resource.MustParse("16Mi")) != 0 {
			return false
		}
	}
	if volumes != 1 {
		return false
	}
	seen := map[string]bool{}
	for _, container := range pod.Spec.Containers {
		wantProbe := vitessTabletReadinessProbe(container.Name)
		if wantProbe == nil {
			continue
		}
		if seen[container.Name] || !reflect.DeepEqual(container.ReadinessProbe, wantProbe) {
			return false
		}
		seen[container.Name] = true
		mounts := 0
		for _, mount := range container.VolumeMounts {
			if mount.Name != vitessSocketVolumeName && mount.MountPath != vitessSocketMountPath {
				continue
			}
			mounts++
			if mount.Name != vitessSocketVolumeName || mount.MountPath != vitessSocketMountPath || mount.ReadOnly || mount.SubPath != "" || mount.SubPathExpr != "" {
				return false
			}
		}
		if mounts != 1 {
			return false
		}
	}
	return seen["mysqld"] && seen["vttablet"] && len(seen) == 2
}
