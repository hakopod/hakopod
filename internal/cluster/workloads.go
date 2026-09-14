package cluster

import (
	"context"
	"fmt"
	"github.com/hakopod/hakopod/internal/spec"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sort"
	"strconv"
)

func workloadQuota(q *corev1.ResourceQuota, a spec.Application) {
	volumes, gpus := false, false
	for _, s := range a.Services {
		volumes = volumes || s.Volume != nil || len(s.Mounts) > 0
		gpus = gpus || s.GPU != nil
	}
	// Keep retained volumes usable even when their service is removed. Quota
	// bounds capacity, it does not reserve or allocate disk/memory by itself.
	q.Spec.Hard[corev1.ResourcePersistentVolumeClaims] = resource.MustParse("20")
	q.Spec.Hard[corev1.ResourceRequestsStorage] = resource.MustParse("200Gi")
	if volumes {
		q.Spec.Hard[corev1.ResourceRequestsEphemeralStorage] = resource.MustParse("8Gi")
	}
	if gpus {
		q.Spec.Hard[corev1.ResourceRequestsMemory] = resource.MustParse("64Gi")
		q.Spec.Hard[corev1.ResourceLimitsMemory] = resource.MustParse("128Gi")
		q.Spec.Hard[corev1.ResourceRequestsCPU] = resource.MustParse("32")
		q.Spec.Hard[corev1.ResourceLimitsCPU] = resource.MustParse("64")
		q.Spec.Hard["requests.nvidia.com/gpu"] = resource.MustParse("8")
	}
}

func configureWorkload(d *appsv1.Deployment, s spec.Service) {
	p := &d.Spec.Template.Spec
	c := &p.Containers[0]
	if s.UpdateStrategy == "recreate" {
		d.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	}
	if s.Architecture != "" {
		p.NodeSelector = map[string]string{"kubernetes.io/arch": s.Architecture}
	}
	if s.RunAsUser > 0 {
		p.SecurityContext.RunAsUser = ptr(s.RunAsUser)
		p.SecurityContext.RunAsGroup = ptr(s.RunAsUser)
	}
	p.SecurityContext.FSGroup = ptr(*p.SecurityContext.RunAsUser)
	if s.RunAsGroup > 0 {
		p.SecurityContext.RunAsGroup = ptr(s.RunAsGroup)
	}
	if s.FSGroup > 0 {
		p.SecurityContext.FSGroup = ptr(s.FSGroup)
	}
	c.SecurityContext.ReadOnlyRootFilesystem = ptr(s.ReadOnlyRootFilesystem)
	if s.TerminationGraceSeconds > 0 {
		p.TerminationGracePeriodSeconds = ptr(s.TerminationGraceSeconds)
	}
	p.SecurityContext.FSGroupChangePolicy = ptr(corev1.FSGroupChangeOnRootMismatch)
	if v := s.Volume; v != nil {
		d.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
		p.Volumes = append(p.Volumes, corev1.Volume{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: d.Name + "-data"}}})
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "data", MountPath: v.MountPath})
	}
	if len(s.Mounts) > 0 {
		d.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	}
	seen := map[string]bool{}
	for _, m := range s.Mounts {
		volumeName := "volume-" + m.Volume
		if !seen[volumeName] {
			p.Volumes = append(p.Volumes, corev1.Volume{Name: volumeName, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "hakopod-volume-" + m.Volume}}})
			seen[volumeName] = true
		}
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: volumeName, MountPath: m.MountPath, SubPath: m.SubPath, ReadOnly: m.ReadOnly})
	}
	for i, m := range s.TemporaryMounts {
		name := "temporary-" + strconv.Itoa(i)
		medium := corev1.StorageMediumDefault
		if m.Memory {
			medium = corev1.StorageMediumMemory
		}
		p.Volumes = append(p.Volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: medium, SizeLimit: ptr(resource.MustParse(strconv.FormatInt(m.SizeMiB, 10) + "Mi"))}}})
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: name, MountPath: m.MountPath})
	}
	if g := s.GPU; g != nil {
		c.Resources.Requests["nvidia.com/gpu"] = resource.MustParse(strconv.FormatInt(g.Count, 10))
		c.Resources.Limits["nvidia.com/gpu"] = resource.MustParse(strconv.FormatInt(g.Count, 10))
		p.Tolerations = append(p.Tolerations, corev1.Toleration{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule})
		p.Volumes = append(p.Volumes, corev1.Volume{Name: "shm", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: ptr(resource.MustParse("1Gi"))}}})
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "shm", MountPath: "/dev/shm"})
	}
	keys := make([]string, 0, len(s.Secrets))
	for key := range s.Secrets {
		keys = append(keys, key)
	}
	for key := range s.Bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		c.Env = append(c.Env, corev1.EnvVar{Name: key, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: d.Name + "-environment"}, Key: key}}})
	}
}

func (c *Client) prepareStorage(ctx context.Context, t Target, name string, s spec.Service) error {
	seen := map[string]bool{}
	for _, mount := range s.Mounts {
		if seen[mount.Volume] {
			continue
		}
		seen[mount.Volume] = true
		v, ok := t.Spec.Volumes[mount.Volume]
		if !ok {
			return fmt.Errorf("unknown named volume %s", mount.Volume)
		}
		if err := c.ensureDataClaim(ctx, t, "hakopod-volume-"+mount.Volume, "", v); err != nil {
			return err
		}
	}
	if s.GPU != nil {
		nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 200})
		if err != nil {
			return err
		}
		found := false
		for _, n := range nodes.Items {
			g := n.Status.Allocatable["nvidia.com/gpu"]
			if !n.Spec.Unschedulable && g.Value() >= s.GPU.Count {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no schedulable node advertises the requested NVIDIA GPU capacity; install the device plugin and add a GPU node")
		}
	}
	if s.Volume == nil {
		return nil
	}
	v := s.Volume
	return c.ensureDataClaim(ctx, t, name+"-data", name, spec.NamedVolume{SizeGiB: v.SizeGiB, StorageClass: v.StorageClass, AccessMode: "ReadWriteOnce"})
}

func (c *Client) ensureDataClaim(ctx context.Context, t Target, claimName, serviceName string, v spec.NamedVolume) error {
	api := c.kube.CoreV1().PersistentVolumeClaims(Namespace(t.ApplicationID))
	size := resource.MustParse(strconv.FormatInt(v.SizeGiB, 10) + "Gi")
	mode := corev1.PersistentVolumeAccessMode(v.AccessMode)
	if mode == "" {
		mode = corev1.ReadWriteOnce
	}
	existing, err := api.Get(ctx, claimName, metav1.GetOptions{})
	if err == nil {
		if err = owned(existing, t); err != nil {
			return err
		}
		if existing.Labels[serviceKey] != serviceName {
			return fmt.Errorf("data claim %s belongs to a different service or volume kind", claimName)
		}
		old := existing.Spec.Resources.Requests[corev1.ResourceStorage]
		if old.Cmp(size) != 0 || (v.StorageClass != "" && (existing.Spec.StorageClassName == nil || *existing.Spec.StorageClassName != v.StorageClass)) || len(existing.Spec.AccessModes) != 1 || existing.Spec.AccessModes[0] != mode {
			return fmt.Errorf("existing data volume size/class/access mode cannot change through a deployment; migrate or expand storage explicitly")
		}
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	var class *string
	if v.StorageClass != "" {
		class = &v.StorageClass
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: claimName, Namespace: Namespace(t.ApplicationID), Labels: labelsFor(t, serviceName), Annotations: map[string]string{"hakopod.io/retain-data": "true"}}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{mode}, StorageClassName: class, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: size}}}}
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	_, err = api.Create(ctx, pvc, metav1.CreateOptions{})
	return err
}
