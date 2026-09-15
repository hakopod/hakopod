package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const fileObjectLabel = "hakopod.io/config-files"

func fileObjects(t Target, name string, s spec.Service) (*corev1.ConfigMap, *corev1.Secret) {
	plain, sensitive := map[string]string{}, map[string][]byte{}
	for key, f := range s.Files {
		if f.Content != nil {
			plain[key] = *f.Content
		} else {
			sensitive[key] = t.secretValues[name][spec.FileSecretKey(key)]
		}
	}
	meta := func(kind string, data any) metav1.ObjectMeta {
		encoded, _ := json.Marshal(data)
		hash := sha256.Sum256(encoded)
		labels := labelsFor(t, name)
		labels[fileObjectLabel] = "true"
		return metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s-%x", name, kind, hash[:8]), Namespace: Namespace(t.ApplicationID), Labels: labels}
	}
	return &corev1.ConfigMap{ObjectMeta: meta("files", plain), Immutable: ptr(true), Data: plain},
		&corev1.Secret{ObjectMeta: meta("files-secret", sensitive), Immutable: ptr(true), Type: corev1.SecretTypeOpaque, Data: sensitive}
}

func configureFiles(t Target, name string, s spec.Service, p *corev1.PodSpec) {
	cm, secret := fileObjects(t, name, s)
	names := make([]string, 0, len(s.Files))
	for key := range s.Files {
		names = append(names, key)
	}
	sort.Strings(names)
	for i, key := range names {
		f := s.Files[key]
		mode := f.Mode
		if mode == 0 {
			mode = 0444
		}
		volume := corev1.Volume{Name: fmt.Sprintf("config-file-%d", i)}
		item := []corev1.KeyToPath{{Key: key, Path: "file", Mode: &mode}}
		if f.Content != nil {
			volume.ConfigMap = &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name}, Items: item}
		} else {
			volume.Secret = &corev1.SecretVolumeSource{SecretName: secret.Name, Items: item}
		}
		p.Volumes = append(p.Volumes, volume)
		p.Containers[0].VolumeMounts = append(p.Containers[0].VolumeMounts, corev1.VolumeMount{Name: volume.Name, MountPath: f.MountPath, SubPath: "file", ReadOnly: true})
	}
}

func (c *Client) prepareFiles(ctx context.Context, t Target, name string, s spec.Service) error {
	for key, f := range s.Files {
		if f.Secret != nil {
			if _, ok := t.secretValues[name][spec.FileSecretKey(key)]; !ok {
				return fmt.Errorf("%s: file secret snapshot is incomplete", name)
			}
		}
	}
	cm, secret := fileObjects(t, name, s)
	if len(cm.Data) > 0 {
		api := c.kube.CoreV1().ConfigMaps(cm.Namespace)
		current, err := api.Get(ctx, cm.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if err := beforeStep(ctx, t); err != nil {
				return err
			}
			_, err = api.Create(ctx, cm, metav1.CreateOptions{})
		} else if err == nil {
			err = owned(current, t)
			if err == nil && (current.Immutable == nil || !*current.Immutable || !reflect.DeepEqual(current.Data, cm.Data)) {
				err = fmt.Errorf("configuration snapshot integrity check failed")
			}
		}
		if err != nil {
			return err
		}
	}
	if len(secret.Data) > 0 {
		api := c.kube.CoreV1().Secrets(secret.Namespace)
		current, err := api.Get(ctx, secret.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if err := beforeStep(ctx, t); err != nil {
				return err
			}
			_, err = api.Create(ctx, secret, metav1.CreateOptions{})
		} else if err == nil {
			err = owned(current, t)
			if err == nil && (current.Immutable == nil || !*current.Immutable || !reflect.DeepEqual(current.Data, secret.Data)) {
				err = fmt.Errorf("secret file snapshot integrity check failed")
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Prune only unused owned file snapshots. Active and terminating pods keep
// their old files through rollouts; retained Jobs also keep their snapshots.
func (c *Client) cleanupFiles(ctx context.Context, t Target) error {
	ns := Namespace(t.ApplicationID)
	selector := managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID)
	used := map[string]bool{}
	mark := func(p corev1.PodSpec) {
		for _, v := range p.Volumes {
			if v.ConfigMap != nil {
				used[v.ConfigMap.Name] = true
			}
			if v.Secret != nil {
				used[v.Secret.SecretName] = true
			}
		}
	}
	pods, err := c.kube.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 401})
	if err != nil {
		return err
	}
	if pods.Continue != "" || len(pods.Items) > 400 {
		return fmt.Errorf("too many pods for file cleanup")
	}
	for _, p := range pods.Items {
		mark(p.Spec)
	}
	deps, err := c.kube.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 101})
	if err != nil {
		return err
	}
	if deps.Continue != "" || len(deps.Items) > 100 {
		return fmt.Errorf("too many deployments for file cleanup")
	}
	for _, d := range deps.Items {
		mark(d.Spec.Template.Spec)
	}
	jobs, err := c.kube.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 201})
	if err != nil {
		return err
	}
	if jobs.Continue != "" || len(jobs.Items) > 200 {
		return fmt.Errorf("too many jobs for file cleanup")
	}
	for _, j := range jobs.Items {
		mark(j.Spec.Template.Spec)
	}
	schedules, e := c.kube.BatchV1().CronJobs(ns).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 21})
	if e != nil {
		return e
	}
	if schedules.Continue != "" || len(schedules.Items) > 20 {
		return fmt.Errorf("too many scheduled jobs for file cleanup")
	}
	for _, j := range schedules.Items {
		mark(j.Spec.JobTemplate.Spec.Template.Spec)
	}
	opts := metav1.ListOptions{LabelSelector: selector + "," + fileObjectLabel + "=true", Limit: 401}
	cms, err := c.kube.CoreV1().ConfigMaps(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if cms.Continue != "" || len(cms.Items) > 400 {
		return fmt.Errorf("too many file snapshots; administrator cleanup required")
	}
	for _, v := range cms.Items {
		if !used[v.Name] {
			if err := beforeStep(ctx, t); err != nil {
				return err
			}
			if err := owned(&v, t); err != nil {
				return err
			}
			if err := c.kube.CoreV1().ConfigMaps(ns).Delete(ctx, v.Name, deleteOptions(&v)); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	secrets, err := c.kube.CoreV1().Secrets(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if secrets.Continue != "" || len(secrets.Items) > 400 {
		return fmt.Errorf("too many file snapshots; administrator cleanup required")
	}
	for _, v := range secrets.Items {
		if !used[v.Name] {
			if err := beforeStep(ctx, t); err != nil {
				return err
			}
			if err := owned(&v, t); err != nil {
				return err
			}
			if err := c.kube.CoreV1().Secrets(ns).Delete(ctx, v.Name, deleteOptions(&v)); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}
