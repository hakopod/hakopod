package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const databaseTrustLabel = "hakopod.io/database-trust"
const databaseTrustVolume = "hakopod-database-trust"
const databaseTrustDirectory = "/var/run/secrets/hakopod-database"

var databaseTrustID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// DatabaseTrustPath is stable across certificate renewal. Only the public CA
// is mounted, scoped to the databases explicitly bound to this service.
func DatabaseTrustPath(id string) string { return databaseTrustDirectory + "/" + id + ".crt" }

func databaseTrustName(service string, data map[string]string) string {
	encoded, _ := json.Marshal(struct {
		Service string
		Data    map[string]string
	}{service, data})
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("hp-db-trust-%x", digest[:20])
}

func databaseTrustObject(t Target, name string) *corev1.ConfigMap {
	data := map[string]string{}
	for variable, binding := range t.Spec.Services[name].Bindings {
		if ca := t.databaseConnections[name][variable].CA; binding.ManagedDatabase != "" && ca != "" {
			data[binding.ManagedDatabase+".crt"] = ca
		}
	}
	labels := labelsFor(t, name)
	labels[databaseTrustLabel] = "true"
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: databaseTrustName(name, data), Namespace: Namespace(t.ApplicationID), Labels: labels}, Immutable: ptr(true), Data: data}
}

func applyDatabaseTrustMount(t Target, name string, pod *corev1.PodSpec) {
	cm := databaseTrustObject(t, name)
	if len(cm.Data) == 0 {
		return
	}
	pod.Volumes = append(pod.Volumes, corev1.Volume{Name: databaseTrustVolume, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name}, DefaultMode: ptr(int32(0444))}}})
	pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts, corev1.VolumeMount{Name: databaseTrustVolume, MountPath: databaseTrustDirectory, ReadOnly: true})
}

func (c *Client) prepareDatabaseTrust(ctx context.Context, t Target, name string) error {
	cm := databaseTrustObject(t, name)
	if len(cm.Data) == 0 {
		return nil
	}
	if len(cm.Data) > 64 {
		return fmt.Errorf("service database trust limit exceeded")
	}
	total := 0
	for key, ca := range cm.Data {
		if len(key) != 36 || !databaseTrustID.MatchString(key[:32]) || key[32:] != ".crt" {
			return fmt.Errorf("invalid database trust identity")
		}
		if _, _, err := database.ParsePublicTrust([]byte(ca), time.Now()); err != nil {
			return err
		}
		total += len(ca)
	}
	if total > 512<<10 {
		return fmt.Errorf("service database trust exceeds 512 KiB")
	}
	if err := c.cleanupDatabaseTrust(ctx, t, name, cm.Name); err != nil {
		return err
	}
	api := c.kube.CoreV1().ConfigMaps(cm.Namespace)
	current, err := api.Get(ctx, cm.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		_, err = api.Create(ctx, cm, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if err = databaseTrustOwned(current, t, name); err != nil {
		return err
	}
	if !reflect.DeepEqual(current.Data, cm.Data) {
		return fmt.Errorf("database trust snapshot integrity changed")
	}
	return nil
}

func databaseTrustOwned(cm *corev1.ConfigMap, t Target, name string) error {
	if owned(cm, t) != nil || cm.DeletionTimestamp != nil || cm.Labels[serviceKey] != name || cm.Labels[databaseTrustLabel] != "true" || cm.Immutable == nil || !*cm.Immutable || len(cm.BinaryData) != 0 || cm.Name != databaseTrustName(name, cm.Data) {
		return fmt.Errorf("database trust snapshot ownership or integrity changed")
	}
	return nil
}

// Retain references from old ReplicaSets, running/terminating pods and jobs.
// Lists are bounded and fully read before any deletion; partial lists fail shut.
func (c *Client) cleanupDatabaseTrust(ctx context.Context, t Target, name, keep string) error {
	ns := Namespace(t.ApplicationID)
	items, err := c.kube.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{LabelSelector: managedBy + "=hakopod," + ownerKey + "=" + ownerID(t.ApplicationID) + "," + serviceKey + "=" + name + "," + databaseTrustLabel + "=true", Limit: 65})
	if err != nil {
		return err
	}
	if items.Continue != "" || len(items.Items) > 64 {
		return fmt.Errorf("database trust history exceeds cleanup limit")
	}
	if len(items.Items) == 0 {
		return nil
	}
	used := map[string]bool{keep: true}
	mark := func(p corev1.PodSpec) {
		for _, v := range p.Volumes {
			if v.ConfigMap != nil {
				used[v.ConfigMap.Name] = true
			}
		}
	}
	opts := metav1.ListOptions{Limit: 401}
	pods, err := c.kube.CoreV1().Pods(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if pods.Continue != "" || len(pods.Items) > 400 {
		return fmt.Errorf("too many pods for database trust cleanup")
	}
	for _, p := range pods.Items {
		mark(p.Spec)
	}
	deps, err := c.kube.AppsV1().Deployments(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if deps.Continue != "" || len(deps.Items) > 400 {
		return fmt.Errorf("too many deployments for database trust cleanup")
	}
	for _, d := range deps.Items {
		mark(d.Spec.Template.Spec)
	}
	replicas, err := c.kube.AppsV1().ReplicaSets(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if replicas.Continue != "" || len(replicas.Items) > 400 {
		return fmt.Errorf("too many replica sets for database trust cleanup")
	}
	for _, r := range replicas.Items {
		mark(r.Spec.Template.Spec)
	}
	jobs, err := c.kube.BatchV1().Jobs(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if jobs.Continue != "" || len(jobs.Items) > 400 {
		return fmt.Errorf("too many jobs for database trust cleanup")
	}
	for _, j := range jobs.Items {
		mark(j.Spec.Template.Spec)
	}
	schedules, err := c.kube.BatchV1().CronJobs(ns).List(ctx, opts)
	if err != nil {
		return err
	}
	if schedules.Continue != "" || len(schedules.Items) > 400 {
		return fmt.Errorf("too many schedules for database trust cleanup")
	}
	for _, j := range schedules.Items {
		mark(j.Spec.JobTemplate.Spec.Template.Spec)
	}
	retained := 0
	for _, cm := range items.Items {
		if used[cm.Name] {
			retained++
			continue
		}
		if err = databaseTrustOwned(&cm, t, name); err != nil {
			return err
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		if err = c.kube.CoreV1().ConfigMaps(ns).Delete(ctx, cm.Name, deleteOptions(&cm)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if retained >= 64 {
		return fmt.Errorf("database trust history is full; retire unused workloads before renewal")
	}
	return nil
}

// RenewDatabaseTrust runs under the application's durable maintenance claim.
// It changes only the CA volume reference. One-off Jobs retain their immutable
// snapshot; scheduled Jobs receive renewed trust for their next run.
func (c *Client) RenewDatabaseTrust(ctx context.Context, t Target, emit func(Event), name string) error {
	svc, exists := t.Spec.Services[name]
	if !exists {
		return fmt.Errorf("database trust service is unavailable")
	}
	if svc.Job != nil && svc.Job.Schedule == nil {
		return nil
	}
	bound := false
	for _, b := range svc.Bindings {
		bound = bound || b.ManagedDatabase != ""
	}
	if !bound {
		return nil
	}
	selected := t
	selected.Spec.Services = map[string]spec.Service{name: svc}
	if err := c.snapshotDatabaseBindings(ctx, &selected); err != nil {
		return err
	}
	t.databaseConnections = selected.databaseConnections
	wanted := databaseTrustObject(t, name)
	if len(wanted.Data) == 0 {
		return nil
	}
	renew := func(pod *corev1.PodSpec) (bool, error) {
		volume, mount := -1, false
		for i, v := range pod.Volumes {
			if v.Name == databaseTrustVolume {
				if volume != -1 || v.ConfigMap == nil {
					return false, fmt.Errorf("database trust volume changed; redeploy the accepted revision")
				}
				volume = i
			}
		}
		if volume == -1 || len(pod.Containers) == 0 {
			return false, fmt.Errorf("database trust volume is missing; redeploy the accepted revision")
		}
		for _, m := range pod.Containers[0].VolumeMounts {
			if m.Name == databaseTrustVolume {
				if m.MountPath != databaseTrustDirectory || !m.ReadOnly || m.SubPath != "" || m.SubPathExpr != "" {
					return false, fmt.Errorf("database trust mount changed")
				}
				mount = true
			}
		}
		v := &pod.Volumes[volume]
		if !mount || len(v.ConfigMap.Items) != 0 || v.ConfigMap.DefaultMode == nil || *v.ConfigMap.DefaultMode != 0444 || v.ConfigMap.Optional != nil && *v.ConfigMap.Optional {
			return false, fmt.Errorf("database trust mount permissions changed")
		}
		old, err := c.kube.CoreV1().ConfigMaps(Namespace(t.ApplicationID)).Get(ctx, v.ConfigMap.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if err = databaseTrustOwned(old, t, name); err != nil {
			return false, err
		}
		if len(old.Data) != len(wanted.Data) {
			return false, fmt.Errorf("database bindings changed; redeploy the accepted revision")
		}
		for key := range old.Data {
			if _, ok := wanted.Data[key]; !ok {
				return false, fmt.Errorf("database bindings changed; redeploy the accepted revision")
			}
		}
		if v.ConfigMap.Name == wanted.Name {
			return false, c.cleanupDatabaseTrust(ctx, t, name, wanted.Name)
		}
		if err = c.prepareDatabaseTrust(ctx, t, name); err != nil {
			return false, err
		}
		v.ConfigMap.Name = wanted.Name
		return true, nil
	}
	check := func(obj metav1.Object, revisionKey string) error {
		if owned(obj, t) != nil || obj.GetDeletionTimestamp() != nil || obj.GetLabels()[serviceKey] != name || obj.GetAnnotations()[revisionKey] != strconv.FormatInt(t.Revision, 10) {
			return fmt.Errorf("database trust renewal deferred until the accepted deployment is current")
		}
		return nil
	}
	if svc.Job != nil {
		api := c.kube.BatchV1().CronJobs(Namespace(t.ApplicationID))
		current, err := api.Get(ctx, scheduledJobName(name), metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err = check(current, jobRevision); err != nil {
			return err
		}
		changed, err := renew(&current.Spec.JobTemplate.Spec.Template.Spec)
		if err != nil || !changed {
			return err
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		if _, err = api.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return err
		}
	} else {
		api := c.kube.AppsV1().Deployments(Namespace(t.ApplicationID))
		current, err := api.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err = check(current, "hakopod.io/revision"); err != nil {
			return err
		}
		changed, err := renew(&current.Spec.Template.Spec)
		if err != nil || !changed {
			return err
		}
		if err = beforeStep(ctx, t); err != nil {
			return err
		}
		if _, err = api.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	if emit != nil {
		emit(Event{Type: "database_trust_renewed", Service: name, Message: "Public database CA renewed in the workload template. New pods use the updated trust."})
	}
	return nil
}
