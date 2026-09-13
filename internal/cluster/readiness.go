package cluster

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var readinessImagePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*@sha256:[a-f0-9]{64}$`)

func ValidateReadinessProbeImage(image string) error {
	if image != "" && (len(image) > 512 || !readinessImagePattern.MatchString(image) || strings.Contains(image, "://")) {
		return fmt.Errorf("readiness probe image must be a digest-pinned OCI image without credentials")
	}
	return nil
}

func (c *Client) ValidateReadiness(app spec.Application) error {
	for _, s := range app.Services {
		if spec.NeedsReadinessHelper(s) && (c.options.ReadinessProbeImage == "" || ValidateReadinessProbeImage(c.options.ReadinessProbeImage) != nil) {
			return fmt.Errorf("SMTP or combined readiness requires an administrator-configured, digest-pinned readiness probe image")
		}
	}
	return nil
}

func configureReadiness(svc spec.Service, pod *corev1.PodSpec, image string) {
	r := svc.Readiness
	if r == nil {
		return
	}
	probe := &corev1.Probe{TimeoutSeconds: r.TimeoutSeconds, PeriodSeconds: r.PeriodSeconds, FailureThreshold: r.FailureThreshold, SuccessThreshold: 1}
	if !spec.NeedsReadinessHelper(svc) {
		probe.TCPSocket = &corev1.TCPSocketAction{Port: intstr.FromInt32(r.Port)}
	} else {
		args := []string{spec.ReadinessHelperDirectory + "/hakopod-probe", "--protocol=" + r.Protocol, "--port=" + strconv.Itoa(int(r.Port)), "--timeout=" + strconv.Itoa(int(r.TimeoutSeconds)) + "s"}
		if svc.Healthcheck != "" {
			args = append(args, "--http-port="+strconv.Itoa(int(svc.Port)), "--http-path="+svc.Healthcheck)
		}
		if r.TLSServerName != "" {
			args = append(args, "--tls-server-name="+r.TLSServerName)
		}
		if r.TLSCAFile != "" {
			args = append(args, "--tls-ca-file="+r.TLSCAFile)
		}
		probe.Exec = &corev1.ExecAction{Command: args}
		// Give the helper time to report its own bounded failure before kubelet's
		// process deadline. This does not add a long-running container.
		probe.TimeoutSeconds++
		pod.Volumes = append(pod.Volumes, corev1.Volume{Name: "hakopod-readiness-probe", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr(resource.MustParse("40Mi"))}}})
		pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "hakopod-readiness-probe", MountPath: spec.ReadinessHelperDirectory, ReadOnly: true})
		if pod.SecurityContext.FSGroup == nil {
			pod.SecurityContext.FSGroup = pod.SecurityContext.RunAsGroup
		}
		pod.InitContainers = append(pod.InitContainers, corev1.Container{
			Name: "install-readiness-probe", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"/hakopod-probe", "install"},
			Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi"), corev1.ResourceEphemeralStorage: resource.MustParse("40Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi"), corev1.ResourceEphemeralStorage: resource.MustParse("64Mi")}},
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), ReadOnlyRootFilesystem: ptr(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			VolumeMounts:    []corev1.VolumeMount{{Name: "hakopod-readiness-probe", MountPath: "/probe"}},
		})
	}
	pod.Containers[0].ReadinessProbe = probe
	if pod.Containers[0].StartupProbe == nil {
		pod.Containers[0].StartupProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(r.Port)}}, PeriodSeconds: 2, TimeoutSeconds: 2, FailureThreshold: 60}
	}
}
