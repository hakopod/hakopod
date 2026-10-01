package cluster

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func (c *Client) clickhouseIdentityController(ctx context.Context, d database.Resource) (*unstructured.Unstructured, error) {
	if d.Spec.Engine != "clickhouse" || c.kube == nil || c.dynamic == nil {
		return nil, fmt.Errorf("ClickHouse identity runtime is unavailable")
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.DeletionTimestamp != nil || ns.Labels[managedBy] != "hakopod" || ns.Labels[databaseOwner] != d.ID {
		return nil, fmt.Errorf("ClickHouse identity namespace changed")
	}
	object, err := c.dynamic.Resource(clickhouseDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetUID() == "" || object.GetDeletionTimestamp() != nil || object.GetLabels()[managedBy] != "hakopod" || object.GetLabels()[databaseOwner] != d.ID || object.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return nil, fmt.Errorf("ClickHouse identity controller or revision changed")
	}
	return object, nil
}

func clickhouseIdentityProjectionMatches(spec corev1.PodSpec, name, directory string) bool {
	volumes, mounts := 0, 0
	for _, volume := range spec.Volumes {
		if volume.Name != name {
			continue
		}
		volumes++
		if volume.Secret == nil || volume.Secret.SecretName != name || len(volume.Secret.Items) != 0 || volume.Secret.DefaultMode == nil || *volume.Secret.DefaultMode != 0440 || volume.Secret.Optional != nil && *volume.Secret.Optional {
			return false
		}
	}
	for _, container := range spec.Containers {
		for _, mount := range container.VolumeMounts {
			if mount.Name != name {
				if mount.MountPath == directory || strings.HasPrefix(mount.MountPath, directory+"/") || strings.HasPrefix(directory, strings.TrimSuffix(mount.MountPath, "/")+"/") {
					return false
				}
				continue
			}
			mounts++
			if container.Name != "clickhouse" || !mount.ReadOnly || mount.MountPath != directory || mount.SubPath != "" || mount.SubPathExpr != "" {
				return false
			}
		}
	}
	return volumes == 1 && mounts == 1
}

func clickhouseClientIdentityPodSpecMatches(spec corev1.PodSpec) bool {
	return len(spec.Containers) == 1 && len(spec.InitContainers) == 0 && spec.Containers[0].Name == "clickhouse" && spec.Containers[0].Image == clickhouseServerImage && clickhouseIdentityProjectionMatches(spec, "database-tls", "/etc/hakopod-tls") && clickhouseIdentityProjectionMatches(spec, clickhouseClientTLSSecret, "/etc/hakopod-client-tls")
}

func clickhouseClientIdentityPodMatches(pod corev1.Pod) bool {
	return clickhouseClientIdentityPodSpecMatches(pod.Spec)
}

func clickhouseIdentityTemplate(object *unstructured.Unstructured) ([]any, int, corev1.PodSpec, error) {
	var pod corev1.PodSpec
	if object == nil {
		return nil, 0, pod, fmt.Errorf("ClickHouse identity configuration is unavailable")
	}
	templates, found, err := unstructured.NestedSlice(object.Object, "spec", "templates", "podTemplates")
	if err != nil || !found || len(templates) != 1 {
		return nil, 0, pod, fmt.Errorf("ClickHouse identity template changed")
	}
	template, ok := templates[0].(map[string]any)
	if !ok || template["name"] != "managed" {
		return nil, 0, pod, fmt.Errorf("ClickHouse identity template changed")
	}
	spec, ok := template["spec"].(map[string]any)
	if !ok || runtime.DefaultUnstructuredConverter.FromUnstructured(spec, &pod) != nil {
		return nil, 0, pod, fmt.Errorf("ClickHouse identity template is invalid")
	}
	return templates, 0, pod, nil
}

func clickhouseClientIdentityConfigured(object *unstructured.Unstructured, d database.Resource) error {
	_, _, pod, err := clickhouseIdentityTemplate(object)
	if err != nil {
		return err
	}
	configuration, found, err := unstructured.NestedString(object.Object, "spec", "configuration", "files", "zz-hakopod.xml")
	if err != nil || !found || configuration != clickhouseServerConfiguration(d) || !clickhouseClientIdentityPodSpecMatches(pod) {
		return fmt.Errorf("ClickHouse data-client identity migration has not converged")
	}
	return nil
}

// An existing cluster first moves its data pods to the separate client leaf.
// This maintenance update may replace data pods, so observation must converge
// before a public endpoint review can be accepted. Later public SAN changes
// only update the client Secret and never change Keeper's identity template.
func (c *Client) reconcileClickHouseClientIdentityConfiguration(ctx context.Context, d database.Resource, before func() error) error {
	object, err := c.clickhouseIdentityController(ctx, d)
	if err != nil {
		return err
	}
	if clickhouseClientIdentityConfigured(object, d) == nil {
		return nil
	}
	templates, index, pod, err := clickhouseIdentityTemplate(object)
	if err != nil {
		return err
	}
	configuration, found, err := unstructured.NestedString(object.Object, "spec", "configuration", "files", "zz-hakopod.xml")
	legacy := strings.ReplaceAll(clickhouseServerConfiguration(d), "/etc/hakopod-client-tls/", "/etc/hakopod-tls/")
	if err != nil || !found || configuration != legacy || len(pod.Containers) != 1 || len(pod.InitContainers) != 0 || pod.Containers[0].Name != "clickhouse" || pod.Containers[0].Image != clickhouseServerImage || !clickhouseIdentityProjectionMatches(pod, "database-tls", "/etc/hakopod-tls") {
		return fmt.Errorf("ClickHouse identity migration refuses unrecognized configuration")
	}
	for _, volume := range pod.Volumes {
		if volume.Name == clickhouseClientTLSSecret {
			return fmt.Errorf("ClickHouse identity migration found a conflicting volume")
		}
	}
	for _, mount := range pod.Containers[0].VolumeMounts {
		if mount.Name == clickhouseClientTLSSecret || mount.MountPath == "/etc/hakopod-client-tls" || strings.HasPrefix(mount.MountPath, "/etc/hakopod-client-tls/") || strings.HasPrefix("/etc/hakopod-client-tls", strings.TrimSuffix(mount.MountPath, "/")+"/") {
			return fmt.Errorf("ClickHouse identity migration found a conflicting mount")
		}
	}
	mode := int32(0440)
	pod.Volumes = append(pod.Volumes, corev1.Volume{Name: clickhouseClientTLSSecret, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: clickhouseClientTLSSecret, DefaultMode: &mode}}})
	pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts, corev1.VolumeMount{Name: clickhouseClientTLSSecret, MountPath: "/etc/hakopod-client-tls", ReadOnly: true})
	converted, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pod)
	if err != nil {
		return fmt.Errorf("ClickHouse identity migration could not prepare the owned template")
	}
	templates[index].(map[string]any)["spec"] = converted
	updated := object.DeepCopy()
	if err = unstructured.SetNestedSlice(updated.Object, templates, "spec", "templates", "podTemplates"); err != nil {
		return err
	}
	if err = unstructured.SetNestedField(updated.Object, clickhouseServerConfiguration(d), "spec", "configuration", "files", "zz-hakopod.xml"); err != nil {
		return err
	}
	if err = before(); err != nil {
		return err
	}
	_, err = c.dynamic.Resource(clickhouseDatabaseResource).Namespace(object.GetNamespace()).Update(ctx, updated, metav1.UpdateOptions{})
	return err
}
