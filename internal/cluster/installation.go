package cluster

import (
	"context"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sort"
	"time"
)

type InstallationSetup struct {
	ReadinessImage      string   `json:"readiness_image"`
	StorageClasses      []string `json:"storage_classes"`
	DefaultStorageClass string   `json:"default_storage_class"`
}

func (c *Client) InstallationSetup(ctx context.Context) (InstallationSetup, error) {
	result := InstallationSetup{ReadinessImage: c.options.ReadinessProbeImage, StorageClasses: []string{}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	classes, err := c.kube.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{Limit: 100})
	if err != nil {
		return result, err
	}
	if classes.Continue != "" {
		return result, fmt.Errorf("too many storage classes; inspect cluster storage")
	}
	for _, v := range classes.Items {
		result.StorageClasses = append(result.StorageClasses, v.Name)
		if v.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			result.DefaultStorageClass = v.Name
		}
	}
	sort.Strings(result.StorageClasses)
	return result, nil
}
func (c *Client) validateStorage(ctx context.Context, t Target) error {
	claims := map[string]string{}
	for name, svc := range t.Spec.Services {
		if svc.Volume != nil {
			claims[name+"-data"] = svc.Volume.StorageClass
		}
		for _, m := range svc.Mounts {
			claims["hakopod-volume-"+m.Volume] = t.Spec.Volumes[m.Volume].StorageClass
		}
	}
	if len(claims) == 0 {
		return nil
	}
	policy, err := c.workloadPolicy(ctx, t)
	if err != nil {
		return err
	}
	if policy != nil && policy.StorageClass != "" {
		for name, class := range claims {
			if class != "" && class != policy.StorageClass {
				return fmt.Errorf("storage class is managed by the runtime")
			}
			claims[name] = policy.StorageClass
		}
	}
	setup, err := c.InstallationSetup(ctx)
	if err != nil {
		return err
	}
	classes := map[string]bool{}
	for _, name := range setup.StorageClasses {
		classes[name] = true
	}
	for volumeName, volume := range t.Spec.Volumes {
		if volume.AccessMode != "ReadWriteMany" {
			continue
		}
		class, err := c.kube.StorageV1().StorageClasses().Get(ctx, volume.StorageClass, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("volumes.%s: shared storage class is unavailable; configure ReadWriteMany storage in Infrastructure > Setup", volumeName)
		}
		if class.Provisioner == "rancher.io/local-path" || class.Provisioner == "kubernetes.io/aws-ebs" || class.Provisioner == "ebs.csi.aws.com" {
			return fmt.Errorf("volumes.%s: storage class %s cannot provide shared ReadWriteMany storage; configure a shared filesystem or use supported object storage", volumeName, class.Name)
		}
	}
	for name, class := range claims {
		if t.ApplicationID != "" {
			claim, err := c.kube.CoreV1().PersistentVolumeClaims(Namespace(t.ApplicationID)).Get(ctx, name, metav1.GetOptions{})
			if err == nil && claim.Status.Phase == corev1.ClaimBound {
				continue
			}
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		if class == "" {
			class = setup.DefaultStorageClass
		}
		if class == "" || !classes[class] {
			return fmt.Errorf("persistent storage is unavailable for %s; ask the installation owner to configure storage in Infrastructure > Setup before deploying", name)
		}
	}
	return nil
}
