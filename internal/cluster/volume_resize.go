package cluster

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

//go:embed volume_copy.py
var volumeCopyScript string

const volumeCopyImage = "python:3.13.15-alpine3.24@sha256:79e7a9b9ff1cbceff819f856fb374477792a5967759d94df266de7b7b4120e6f"
const resizeKey = "hakopod.io/volume-resize"

type VolumeResizeJournal struct {
	SourceUID   types.UID `json:"source_uid"`
	SourcePV    string    `json:"source_pv"`
	SourcePVUID types.UID `json:"source_pv_uid"`
	TargetUID   types.UID `json:"target_uid,omitempty"`
	HelperUID   types.UID `json:"helper_uid,omitempty"`
	Verified    bool      `json:"verified"`
	Retry       bool      `json:"retry,omitempty"`
	Files       int64     `json:"files,omitempty"`
	Bytes       int64     `json:"bytes,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
}

func (c *Client) InspectVolumeResize(ctx context.Context, t Target, p spec.VolumeResize) (VolumeResizeJournal, error) {
	var j VolumeResizeJournal
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(t.ApplicationID), metav1.GetOptions{})
	if err != nil {
		return j, err
	}
	if err = owned(ns, t); err != nil {
		return j, err
	}
	pvc, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).Get(ctx, p.Claim, metav1.GetOptions{})
	if err != nil {
		return j, err
	}
	if err = owned(pvc, t); err != nil {
		return j, err
	}
	if pvc.DeletionTimestamp != nil || pvc.Status.Phase != corev1.ClaimBound || pvc.Spec.VolumeName == "" || pvc.Spec.VolumeMode != nil && *pvc.Spec.VolumeMode != corev1.PersistentVolumeFilesystem {
		return j, fmt.Errorf("resize requires a bound filesystem volume")
	}
	quantity := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if quantity.Cmp(resource.MustParse(strconv.FormatInt(p.OldGiB, 10)+"Gi")) != 0 {
		return j, fmt.Errorf("volume size changed; review resize again")
	}
	pv, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		return j, err
	}
	ref := pv.Spec.ClaimRef
	if ref == nil || ref.UID != pvc.UID || ref.Namespace != ns.Name || ref.Name != p.Claim {
		return j, fmt.Errorf("volume ownership changed")
	}
	j.SourceUID = pvc.UID
	j.SourcePV = pv.Name
	j.SourcePVUID = pv.UID
	// Host-local filesystems and CSI filesystems share this migration path; no
	// expansion support, privileged container or cloud credential is required.
	for _, name := range p.Services {
		d, err := c.kube.AppsV1().Deployments(ns.Name).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return j, err
		}
		if err = owned(d, t); err != nil {
			return j, err
		}
		if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 {
			return j, fmt.Errorf("wait for every affected service to run one replica before resizing")
		}
	}
	return j, nil
}

func (c *Client) checkResizeSource(ctx context.Context, t Target, p spec.VolumeResize, j *VolumeResizeJournal) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	pvc, err := c.kube.CoreV1().PersistentVolumeClaims(Namespace(t.ApplicationID)).Get(ctx, p.Claim, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = owned(pvc, t); err != nil {
		return err
	}
	if pvc.UID != j.SourceUID || pvc.Spec.VolumeName != j.SourcePV || pvc.DeletionTimestamp != nil {
		return fmt.Errorf("original volume identity changed; refusing migration")
	}
	pv, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, j.SourcePV, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if pv.UID != j.SourcePVUID || pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.UID != j.SourceUID {
		return fmt.Errorf("original disk identity changed; refusing migration")
	}
	return nil
}

// PrepareVolumeResize advances one bounded step. Kubernetes runs the potentially
// long copy; controller restarts and API cancellation never lose its identity.
func (c *Client) PrepareVolumeResize(ctx context.Context, t Target, next spec.Application, p spec.VolumeResize, j *VolumeResizeJournal) (bool, error) {
	if err := c.checkResizeSource(ctx, t, p, j); err != nil {
		return false, err
	}
	base := t
	t.BeforeStep = func(step context.Context) error { return c.checkResizeSource(step, base, p, j) }
	ns := Namespace(t.ApplicationID)
	claims := c.kube.CoreV1().PersistentVolumeClaims(ns)
	source, err := claims.Get(ctx, p.Claim, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	target, err := claims.Get(ctx, p.TargetClaim, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if j.TargetUID != "" {
			return false, fmt.Errorf("staging volume disappeared; original is preserved")
		}
		labels := labelsFor(t, "")
		labels[resizeKey] = t.OperationID
		target = &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: p.TargetClaim, Namespace: ns, Labels: labels, Annotations: map[string]string{"hakopod.io/retain-data": "true"}}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: source.Spec.AccessModes, StorageClassName: source.Spec.StorageClassName, VolumeMode: source.Spec.VolumeMode, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(strconv.FormatInt(p.SizeGiB, 10) + "Gi")}}}}
		if err = beforeStep(ctx, t); err != nil {
			return false, err
		}
		target, err = claims.Create(ctx, target, metav1.CreateOptions{})
	}
	if err != nil {
		return false, err
	}
	if err = owned(target, t); err != nil {
		return false, err
	}
	if target.DeletionTimestamp != nil || target.Spec.Resources.Requests.Storage().Cmp(resource.MustParse(strconv.FormatInt(p.SizeGiB, 10)+"Gi")) != 0 || target.Labels[resizeKey] != t.OperationID || j.TargetUID != "" && j.TargetUID != target.UID {
		return false, fmt.Errorf("staging volume ownership changed")
	}
	j.TargetUID = target.UID
	if j.Verified {
		done, err := c.RemoveResizeHelper(ctx, t, j)
		return done, err
	}
	// Stop all readers and writers before reading a single application file.
	wanted := map[string]bool{}
	for _, name := range p.Services {
		wanted[name] = true
	}
	hpas, err := c.kube.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{Limit: 101})
	if err != nil {
		return false, err
	}
	if len(hpas.Items) > 100 || hpas.Continue != "" {
		return false, fmt.Errorf("autoscaler inventory exceeds migration bounds")
	}
	for _, h := range hpas.Items {
		if wanted[h.Spec.ScaleTargetRef.Name] {
			return false, fmt.Errorf("disable autoscaling before volume maintenance")
		}
	}
	for _, name := range p.Services {
		d, err := c.kube.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if err = owned(d, t); err != nil {
			return false, err
		}
		if d.Spec.Replicas == nil || *d.Spec.Replicas != 0 {
			d.Spec.Replicas = ptr(int32(0))
			if err = beforeStep(ctx, t); err != nil {
				return false, err
			}
			if _, err = c.kube.AppsV1().Deployments(ns).Update(ctx, d, metav1.UpdateOptions{}); err != nil {
				return false, err
			}
		}
	}
	pods, err := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: 1001})
	if err != nil {
		return false, err
	}
	if len(pods.Items) > 1000 || pods.Continue != "" {
		return false, fmt.Errorf("pod inventory exceeds migration bounds")
	}
	name := "volume-resize-" + t.OperationID
	for _, pod := range pods.Items {
		if pod.Name == name && pod.Labels[resizeKey] == t.OperationID {
			continue
		}
		for _, v := range pod.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && (v.PersistentVolumeClaim.ClaimName == p.Claim || v.PersistentVolumeClaim.ClaimName == p.TargetClaim) {
				if !wanted[pod.Labels[serviceKey]] || owned(&pod, t) != nil {
					return false, fmt.Errorf("another workload uses this volume; stop it before resizing")
				}
				return false, nil
			}
		}
	}
	cm, err := c.kube.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		labels := labelsFor(t, "")
		labels[resizeKey] = t.OperationID
		cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels}, Immutable: ptr(true), Data: map[string]string{"copy.py": volumeCopyScript}}
		if err = beforeStep(ctx, t); err != nil {
			return false, err
		}
		cm, err = c.kube.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{})
	}
	if err != nil {
		return false, err
	}
	if owned(cm, t) != nil || cm.Labels[resizeKey] != t.OperationID || cm.Data["copy.py"] != volumeCopyScript || cm.Immutable == nil || !*cm.Immutable {
		return false, fmt.Errorf("migration helper configuration ownership changed")
	}
	pod, err := c.kube.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if j.HelperUID != "" {
			j.HelperUID = ""
		}
		pod = resizeHelperPod(t, p, j)
		if err = beforeStep(ctx, t); err != nil {
			return false, err
		}
		pod, err = c.kube.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	}
	if err != nil {
		return false, err
	}
	if owned(pod, t) != nil || pod.Labels[resizeKey] != t.OperationID || pod.Annotations["hakopod.io/source-uid"] != string(j.SourceUID) || pod.Annotations["hakopod.io/target-uid"] != string(j.TargetUID) || j.HelperUID != "" && j.HelperUID != pod.UID {
		return false, fmt.Errorf("migration helper identity changed")
	}
	if err = validateResizeHelper(pod, resizeHelperPod(t, p, j)); err != nil {
		return false, err
	}
	j.HelperUID = pod.UID
	if pod.Status.Phase == corev1.PodFailed {
		if j.Retry {
			j.Retry = false
			_, err = c.RemoveResizeHelper(ctx, t, j)
			return false, err
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == "copy" && status.State.Terminated != nil {
				var report struct {
					Error string `json:"error"`
				}
				if json.Unmarshal([]byte(status.State.Terminated.Message), &report) == nil && report.Error != "" && len(report.Error) < 512 {
					return false, fmt.Errorf("offline copy stopped: %s. Cancel to choose another size, or correct the issue and retry", report.Error)
				}
			}
		}
		return false, fmt.Errorf("offline copy failed; original volume is preserved. Inspect capacity and filesystem permissions, then retry or cancel")
	}
	if pod.Status.Phase != corev1.PodSucceeded {
		return false, nil
	}
	var report struct {
		Verified  bool   `json:"verified"`
		Operation string `json:"operation"`
		Files     int64  `json:"files"`
		Bytes     int64  `json:"bytes"`
		SHA256    string `json:"sha256"`
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "copy" && status.State.Terminated != nil && status.State.Terminated.ExitCode == 0 {
			if err = json.Unmarshal([]byte(status.State.Terminated.Message), &report); err != nil {
				return false, fmt.Errorf("copy verification report is missing")
			}
		}
	}
	if !report.Verified || report.Operation != t.OperationID || len(report.SHA256) != 64 {
		return false, fmt.Errorf("copy verification report is invalid")
	}
	j.Verified = true
	j.Files = report.Files
	j.Bytes = report.Bytes
	j.SHA256 = report.SHA256
	return false, nil // Persist verification before deleting the helper or switching writers.
}

func (c *Client) RemoveResizeHelper(ctx context.Context, t Target, j *VolumeResizeJournal) (bool, error) {
	name := "volume-resize-" + t.OperationID
	ns := Namespace(t.ApplicationID)
	pod, err := c.kube.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if owned(pod, t) != nil || pod.Labels[resizeKey] != t.OperationID || j.HelperUID != "" && j.HelperUID != pod.UID {
			return false, fmt.Errorf("migration helper ownership changed")
		}
		if err = beforeStep(ctx, t); err != nil {
			return false, err
		}
		if pod.DeletionTimestamp == nil {
			err = c.kube.CoreV1().Pods(ns).Delete(ctx, name, deleteOptions(pod))
		}
		return false, err
	}
	if !apierrors.IsNotFound(err) {
		return false, err
	}
	j.HelperUID = ""
	cm, err := c.kube.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		if owned(cm, t) != nil || cm.Labels[resizeKey] != t.OperationID {
			return false, fmt.Errorf("migration configuration ownership changed")
		}
		if err = beforeStep(ctx, t); err != nil {
			return false, err
		}
		err = c.kube.CoreV1().ConfigMaps(ns).Delete(ctx, name, deleteOptions(cm))
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	return true, nil
}

func (c *Client) CheckResizeClaim(ctx context.Context, t Target, name string, uid types.UID, allowMissing bool) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	pvc, err := c.kube.CoreV1().PersistentVolumeClaims(Namespace(t.ApplicationID)).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && allowMissing {
		return nil
	}
	if err != nil {
		return err
	}
	if err = owned(pvc, t); err != nil {
		return err
	}
	if pvc.UID != uid || (!allowMissing && pvc.DeletionTimestamp != nil) {
		return fmt.Errorf("volume identity changed; refusing maintenance")
	}
	return nil
}

func (c *Client) ResumeResizeServices(ctx context.Context, t Target, p spec.VolumeResize) error {
	for _, name := range p.Services {
		d, err := c.kube.AppsV1().Deployments(Namespace(t.ApplicationID)).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err = owned(d, t); err != nil {
			return err
		}
		mountsOriginal := false
		for _, v := range d.Spec.Template.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == p.Claim {
				mountsOriginal = true
			}
		}
		if !mountsOriginal {
			return fmt.Errorf("service volume changed; refusing to resume an unreviewed workload")
		}
		if d.Spec.Replicas != nil && *d.Spec.Replicas == 1 {
			continue
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		d.Spec.Replicas = ptr(int32(1))
		if _, err = c.kube.AppsV1().Deployments(Namespace(t.ApplicationID)).Update(ctx, d, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func resizeHelperPod(t Target, p spec.VolumeResize, j *VolumeResizeJournal) *corev1.Pod {
	name := "volume-resize-" + t.OperationID
	ns := Namespace(t.ApplicationID)
	labels := labelsFor(t, "")
	labels[resizeKey] = t.OperationID
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels, Annotations: map[string]string{"hakopod.io/source-uid": string(j.SourceUID), "hakopod.io/target-uid": string(j.TargetUID)}}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: ptr(int64(1800)), AutomountServiceAccountToken: ptr(false), EnableServiceLinks: ptr(false), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr(true), RunAsUser: ptr(p.User), RunAsGroup: ptr(p.Group), FSGroup: ptr(p.FSGroup), FSGroupChangePolicy: ptr(corev1.FSGroupChangeOnRootMismatch), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{{Name: "copy", Image: volumeCopyImage, Command: []string{"python", "-I", "-B", "/script/copy.py", strconv.FormatInt(p.SizeGiB<<30, 10), t.OperationID}, SecurityContext: &corev1.SecurityContext{ReadOnlyRootFilesystem: ptr(true), AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi"), corev1.ResourceEphemeralStorage: resource.MustParse("1Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi"), corev1.ResourceEphemeralStorage: resource.MustParse("16Mi")}}, VolumeMounts: []corev1.VolumeMount{{Name: "source", MountPath: "/source", ReadOnly: true}, {Name: "target", MountPath: "/target"}, {Name: "script", MountPath: "/script", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "source", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: p.Claim, ReadOnly: true}}}, {Name: "target", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: p.TargetClaim}}}, {Name: "script", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}, DefaultMode: ptr(int32(0444))}}}}}}
}

// Compare the executable helper contract, allowing only API-server defaults.
// Admission-injected executables, mounts or credentials must never produce a
// trusted verification report.
func validateResizeHelper(actual, expected *corev1.Pod) error {
	a, e := actual.Spec, expected.Spec
	if a.HostNetwork || a.HostPID || a.HostIPC || len(a.InitContainers) != 0 || len(a.EphemeralContainers) != 0 || len(a.ImagePullSecrets) != 0 ||
		!reflect.DeepEqual(a.SecurityContext, e.SecurityContext) || !reflect.DeepEqual(a.AutomountServiceAccountToken, e.AutomountServiceAccountToken) ||
		!reflect.DeepEqual(a.EnableServiceLinks, e.EnableServiceLinks) || !reflect.DeepEqual(a.ActiveDeadlineSeconds, e.ActiveDeadlineSeconds) ||
		a.RestartPolicy != e.RestartPolicy || !reflect.DeepEqual(a.Volumes, e.Volumes) || len(a.Containers) != 1 {
		return fmt.Errorf("migration helper execution contract changed")
	}
	container := *a.Containers[0].DeepCopy()
	// Kubernetes defaults these fields when the Pod is admitted.
	if container.ImagePullPolicy == corev1.PullIfNotPresent {
		container.ImagePullPolicy = ""
	}
	if container.TerminationMessagePath == corev1.TerminationMessagePathDefault {
		container.TerminationMessagePath = ""
	}
	if container.TerminationMessagePolicy == corev1.TerminationMessageReadFile {
		container.TerminationMessagePolicy = ""
	}
	if !reflect.DeepEqual(container, e.Containers[0]) {
		return fmt.Errorf("migration helper executable changed")
	}
	return nil
}
