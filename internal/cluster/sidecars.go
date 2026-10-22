package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// sidecarSecret holds the resolved secret values of every sidecar of one
// service. Like config file secrets it is immutable and named by content, so a
// changed value rolls the pod and old pods keep what they started with.
func sidecarSecret(t Target, name string, s spec.Service) *corev1.Secret {
	data := map[string][]byte{}
	for _, sc := range s.Sidecars {
		for key := range sc.Secrets {
			k := spec.SidecarSecretKey(sc.Name, key)
			if v, ok := t.secretValues[name][k]; ok {
				data[k] = v
			}
		}
	}
	if len(data) == 0 {
		return nil
	}
	encoded, _ := json.Marshal(data)
	hash := sha256.Sum256(encoded)
	labels := labelsFor(t, name)
	labels[fileObjectLabel] = "true"
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-sidecar-%x", name, hash[:8]), Namespace: Namespace(t.ApplicationID), Labels: labels}, Immutable: ptr(true), Type: corev1.SecretTypeOpaque, Data: data}
}

// configureSidecars renders native sidecars: init containers that restart
// always, so they start before the service container and run beside it.
func configureSidecars(t Target, name string, s spec.Service, p *corev1.PodSpec) {
	secret := sidecarSecret(t, name, s)
	for _, sc := range s.Sidecars {
		c := corev1.Container{Name: sc.Name, Image: sc.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: sc.Command, Args: sc.Args, RestartPolicy: ptr(corev1.ContainerRestartPolicyAlways),
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(sc.CPU), corev1.ResourceMemory: resource.MustParse(sc.Memory), corev1.ResourceEphemeralStorage: resource.MustParse("16Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(sc.CPU), corev1.ResourceMemory: resource.MustParse(sc.Memory), corev1.ResourceEphemeralStorage: resource.MustParse("128Mi")},
			},
			// The restricted baseline of the service container, set explicitly.
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), RunAsNonRoot: ptr(true), RunAsUser: ptr(*p.SecurityContext.RunAsUser),
				Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		}
		keys := make([]string, 0, len(sc.Env)+len(sc.Secrets))
		for key := range sc.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			c.Env = append(c.Env, corev1.EnvVar{Name: key, Value: sc.Env[key]})
		}
		keys = keys[:0]
		for key := range sc.Secrets {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if secret != nil {
				c.Env = append(c.Env, corev1.EnvVar{Name: key, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret.Name}, Key: spec.SidecarSecretKey(sc.Name, key)}}})
			}
		}
		for _, m := range sc.Mounts {
			volume := "volume-" + m.Volume
			if m.Temporary != "" {
				for i, tm := range s.TemporaryMounts {
					if tm.MountPath == m.Temporary {
						volume = "temporary-" + strconv.Itoa(i)
					}
				}
			}
			c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: volume, MountPath: m.MountPath, ReadOnly: m.ReadOnly})
		}
		p.InitContainers = append(p.InitContainers, c)
	}
}

func (c *Client) prepareSidecarSecrets(ctx context.Context, t Target, name string, s spec.Service) error {
	for _, sc := range s.Sidecars {
		for key := range sc.Secrets {
			if _, ok := t.secretValues[name][spec.SidecarSecretKey(sc.Name, key)]; !ok {
				return fmt.Errorf("%s: sidecar secret snapshot is incomplete", name)
			}
		}
	}
	secret := sidecarSecret(t, name, s)
	if secret == nil {
		return nil
	}
	api := c.kube.CoreV1().Secrets(secret.Namespace)
	current, err := api.Get(ctx, secret.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err := beforeStep(ctx, t); err != nil {
			return err
		}
		_, err = api.Create(ctx, secret, metav1.CreateOptions{})
		return err
	}
	if err == nil {
		err = owned(current, t)
		if err == nil && (current.Immutable == nil || !*current.Immutable || !reflect.DeepEqual(current.Data, secret.Data)) {
			err = fmt.Errorf("sidecar secret snapshot integrity check failed")
		}
	}
	return err
}

// applySidecarGrants widens only sidecars the binding names, and only to what
// the binding lists. Everything else keeps the restricted baseline.
func applySidecarGrants(b NodeProfileBinding, pod *corev1.PodSpec) {
	for i := range pod.InitContainers {
		c := &pod.InitContainers[i]
		g, ok := b.Sidecars[c.Name]
		if !ok || c.RestartPolicy == nil || c.SecurityContext == nil || c.Name == "install-readiness-probe" {
			continue
		}
		for _, capability := range g.Capabilities {
			c.SecurityContext.Capabilities.Add = append(c.SecurityContext.Capabilities.Add, corev1.Capability(capability))
		}
		if g.RunAsRoot {
			c.SecurityContext.RunAsUser, c.SecurityContext.RunAsNonRoot = ptr(int64(0)), ptr(false)
		}
		if g.TUN {
			charDevice := corev1.HostPathCharDev
			pod.Volumes = append(pod.Volumes, corev1.Volume{Name: "hakopod-tun-" + c.Name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev/net/tun", Type: &charDevice}}})
			c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: "hakopod-tun-" + c.Name, MountPath: "/dev/net/tun"})
		}
	}
}
