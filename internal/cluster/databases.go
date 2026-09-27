package cluster

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"k8s.io/apimachinery/pkg/api/resource"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const databaseOwner = "hakopod.io/database-id"
const redisControllerSource = "c5017206e75f7743d79e82db47ec8c39d7410816"

var pgDatabaseResource = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
var redisDatabaseResource = schema.GroupVersionResource{Group: "redis.redis.opstreelabs.in", Version: "v1beta2", Resource: "redis"}
var redisClusterResource = schema.GroupVersionResource{Group: "redis.redis.opstreelabs.in", Version: "v1beta2", Resource: "redisclusters"}
var databaseImages = map[string]string{
	"postgresql:17": "ghcr.io/cloudnative-pg/postgresql:17.6@sha256:30b304a2e300ed80b6d1b740e4369e9b0f25599fb518de78c01fd9f25531791b",
	"postgresql:18": "ghcr.io/cloudnative-pg/postgresql:18.0@sha256:f06cae6ae14e2f101392130dce800b504bf9c5110b5db5fc0266782464882dbb",
	"redis:8":       "quay.io/opstree/redis:v8.2.1@sha256:8027cf7ec625d625a4f60e2526f0073d2b58bf6a6172992015f7699ecbd8504a",
}

func DatabaseNamespace(id string) string { return "hdb-" + id }
func databaseGVR(s database.Spec) (schema.GroupVersionResource, string) {
	if s.Engine == "postgresql" {
		return pgDatabaseResource, "Cluster"
	}
	if s.Mode == "cluster" {
		return redisClusterResource, "RedisCluster"
	}
	return redisDatabaseResource, "Redis"
}
func databaseLabels(d database.Resource) map[string]string {
	return map[string]string{managedBy: "hakopod", databaseOwner: d.ID, "hakopod.io/project": d.Project, "hakopod.io/environment": d.Environment}
}
func (c *Client) DatabaseControllerAvailable(ctx context.Context, s database.Spec) error {
	if c == nil || c.dynamic == nil || c.kube == nil {
		return fmt.Errorf("database controller is unavailable")
	}
	gvr, _ := databaseGVR(s)
	_, err := c.dynamic.Resource(gvr).Namespace("default").List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("the database controller is unavailable or its API is not accessible")
	}
	namespace, name := "cnpg-system", "cnpg-controller-manager"
	if s.Engine == "redis" {
		namespace, name = "redis-operator", "redis-operator"
	}
	deployment, err := c.kube.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || deployment.DeletionTimestamp != nil || deployment.Status.ObservedGeneration < deployment.Generation || deployment.Status.AvailableReplicas < 1 || deployment.Status.UpdatedReplicas < 1 || deployment.Status.UpdatedReplicas != deployment.Status.Replicas || deployment.Status.AvailableReplicas < deployment.Status.UpdatedReplicas || deployment.Status.UnavailableReplicas > 0 {
		return fmt.Errorf("the database controller has no available current deployment")
	}
	if s.Engine == "redis" && !safeRedisController(deployment.Spec.Template) {
		return fmt.Errorf("the Redis controller requires the verified credential-safe build and a bounded 20-minute command timeout")
	}
	return nil
}

func safeRedisController(pod corev1.PodTemplateSpec) bool {
	if pod.Annotations["hakopod.io/redis-controller-source"] != redisControllerSource {
		return false
	}
	for _, container := range pod.Spec.Containers {
		if container.Name != "redis-operator" {
			continue
		}
		name, digest, ok := strings.Cut(container.Image, "@sha256:")
		if _, err := hex.DecodeString(digest); !ok || name == "" || len(digest) != 64 || err != nil {
			return false
		}
		for _, env := range container.Env {
			if env.Name == "EXEC_COMMAND_TIMEOUT" && env.Value == "20m" && env.ValueFrom == nil {
				return true
			}
		}
	}
	return false
}
func DatabaseObject(d database.Resource) (*unstructured.Unstructured, error) {
	if err := d.Spec.Validate(); err != nil {
		return nil, err
	}
	gvr, kind := databaseGVR(d.Spec)
	image, ok := databaseImages[d.Spec.Engine+":"+d.Spec.Version]
	if !ok {
		return nil, fmt.Errorf("no verified database image for this version")
	}
	resources := map[string]any{"requests": map[string]any{"cpu": d.Spec.CPU, "memory": d.Spec.Memory}, "limits": map[string]any{"cpu": d.Spec.CPU, "memory": d.Spec.Memory}}
	var spec map[string]any
	if d.Spec.Engine == "postgresql" {
		spec = map[string]any{"instances": int64(d.Spec.Members()), "imageName": image, "enableSuperuserAccess": false, "resources": resources, "storage": map[string]any{"size": fmt.Sprintf("%dGi", d.Spec.StorageGiB)}, "bootstrap": map[string]any{"initdb": map[string]any{"database": "app", "owner": "app", "secret": map[string]any{"name": "database-credentials"}}}}
	} else {
		spec = map[string]any{"kubernetesConfig": map[string]any{"image": image, "imagePullPolicy": "IfNotPresent", "resources": resources, "redisSecret": map[string]any{"name": "database-credentials", "key": "password"}}, "podSecurityContext": map[string]any{"runAsUser": int64(1000), "fsGroup": int64(1000)}, "storage": map[string]any{"volumeClaimTemplate": map[string]any{"spec": map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", d.Spec.StorageGiB)}}}}}}
		if d.Spec.Mode == "standalone" {
			spec["securityContext"] = map[string]any{"runAsUser": int64(1000), "runAsNonRoot": true, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}
		}
		if d.Spec.Mode == "cluster" {
			// Redis node identity and slot ownership must survive a pod replacement.
			// The image's default /node-conf directory is outside its persistent volume.
			spec["env"] = []any{map[string]any{"name": "NODE_CONF_DIR", "value": "/data/node-conf"}}
			spec["clusterSize"] = int64(d.Spec.Shards)
			spec["clusterVersion"] = "v7"
			spec["persistenceEnabled"] = true
			spec["redisLeader"] = map[string]any{"replicas": int64(d.Spec.Shards)}
			spec["redisFollower"] = map[string]any{"replicas": int64(d.Spec.Shards * d.Spec.Replicas)}
		}
	}
	labels := map[string]any{}
	for k, v := range databaseLabels(d) {
		labels[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": gvr.Group + "/" + gvr.Version, "kind": kind, "metadata": map[string]any{"name": "database", "namespace": DatabaseNamespace(d.ID), "labels": labels, "annotations": map[string]any{"hakopod.io/database-revision": strconv.FormatInt(d.Revision, 10)}}, "spec": spec}}, nil
}

// ApplyDatabase writes only this database's namespace, credentials and controller
// object. Each mutation is fenced by the operation's current lease/authority.
func (c *Client) ApplyDatabase(ctx context.Context, d database.Resource, password []byte, before func() error) error {
	if len(password) < 32 || len(password) > 128 {
		return fmt.Errorf("database credentials are unavailable")
	}
	object, err := c.databaseObject(ctx, d)
	if err != nil {
		return err
	}
	if err = c.DatabaseControllerAvailable(ctx, d.Spec); err != nil {
		return err
	}
	ns := DatabaseNamespace(d.ID)
	existing, err := c.kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		existing, err = c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: databaseLabels(d)}}, metav1.CreateOptions{})
	}
	if err != nil {
		return fmt.Errorf("database namespace could not be reconciled")
	}
	if existing.Labels[databaseOwner] != d.ID || existing.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("database namespace is not owned by this resource")
	}
	secret, err := c.kube.CoreV1().Secrets(ns).Get(ctx, "database-credentials", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		secret, err = c.kube.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "database-credentials", Namespace: ns, Labels: databaseLabels(d)}, Type: corev1.SecretTypeBasicAuth, Immutable: ptr(true), Data: map[string][]byte{"username": []byte("app"), "password": password}}, metav1.CreateOptions{})
	}
	if err != nil {
		return fmt.Errorf("database credentials could not be reconciled")
	}
	if secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("database credentials are not owned by this resource")
	}
	if subtle.ConstantTimeCompare(secret.Data["password"], password) != 1 || string(secret.Data["username"]) != "app" {
		return fmt.Errorf("database credential identity changed")
	}
	if err = c.databaseNetworkPolicy(ctx, d, before); err != nil {
		return err
	}
	gvr, _ := databaseGVR(d.Spec)
	api := c.dynamic.Resource(gvr).Namespace(ns)
	current, err := api.Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = api.Create(ctx, object, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return fmt.Errorf("database controller state is unavailable")
	}
	if current.GetLabels()[databaseOwner] != d.ID || current.GetLabels()[managedBy] != "hakopod" {
		return fmt.Errorf("database controller object has a different owner")
	}
	if current.GetAnnotations()["hakopod.io/database-revision"] == strconv.FormatInt(d.Revision, 10) {
		return nil
	}
	// Preserve controller finalizers, owner references, defaults and status. Only
	// overlay the fields owned by Hakopod on the latest resource version.
	updated := current.DeepCopy()
	overlayDatabaseFields(updated.Object["spec"].(map[string]any), object.Object["spec"].(map[string]any))
	annotations := updated.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["hakopod.io/database-revision"] = strconv.FormatInt(d.Revision, 10)
	updated.SetAnnotations(annotations)
	if err = before(); err != nil {
		return err
	}
	_, err = api.Update(ctx, updated, metav1.UpdateOptions{})
	return err
}

func (c *Client) DeleteDatabase(ctx context.Context, d database.Resource, before func() error) (bool, error) {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// A namespace can disappear before CSI finishes reclaiming its volumes.
		// Keep the durable reservation until the provisioner removes every PV.
		volumes, listErr := c.kube.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{Limit: 1025})
		if listErr != nil {
			return false, listErr
		}
		if len(volumes.Items) > 1024 || volumes.Continue != "" {
			return false, fmt.Errorf("database volume reclamation list exceeds its bound")
		}
		for _, volume := range volumes.Items {
			if volume.Spec.ClaimRef != nil && volume.Spec.ClaimRef.Namespace == DatabaseNamespace(d.ID) {
				return false, nil
			}
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return false, fmt.Errorf("refusing to delete an unowned database namespace")
	}
	if ns.DeletionTimestamp != nil {
		return false, nil
	}
	// The accepted delete explicitly removes this database's data. Change only
	// PVs bound to the exact claims in its owned namespace, never the shared class.
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).List(ctx, metav1.ListOptions{Limit: 1025})
	if err != nil {
		return false, err
	}
	if len(claims.Items) > 1024 || claims.Continue != "" {
		return false, fmt.Errorf("database volume claim list exceeds its bound")
	}
	volumes := make([]*corev1.PersistentVolume, 0, len(claims.Items))
	for _, claim := range claims.Items {
		if claim.Spec.VolumeName == "" {
			continue
		}
		volume, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		ref := volume.Spec.ClaimRef
		if ref == nil || ref.Namespace != ns.Name || ref.Name != claim.Name || ref.UID != claim.UID || claim.UID == "" {
			return false, fmt.Errorf("database volume claim identity changed")
		}
		if volume.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
			volumes = append(volumes, volume)
		}
	}
	for _, volume := range volumes {
		volume.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
		if err = before(); err != nil {
			return false, err
		}
		if _, err = c.kube.CoreV1().PersistentVolumes().Update(ctx, volume, metav1.UpdateOptions{}); err != nil {
			return false, err
		}
	}
	if err = before(); err != nil {
		return false, err
	}
	err = c.kube.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: ptr(ns.UID)}})
	return false, err
}

func (c *Client) ObserveDatabase(ctx context.Context, d database.Resource) (database.Observation, error) {
	o := database.Observation{ObservedAt: time.Now().UTC(), Revision: d.Revision, Status: "pending", Members: []database.Member{}, Endpoints: []database.Endpoint{}}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return o, err
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil {
		return o, fmt.Errorf("database namespace is unavailable")
	}
	if ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return o, fmt.Errorf("database namespace ownership changed")
	}
	gvr, _ := databaseGVR(d.Spec)
	object, err := c.dynamic.Resource(gvr).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		return o, fmt.Errorf("database controller state is unavailable")
	}
	if object.GetLabels()[databaseOwner] != d.ID {
		return o, fmt.Errorf("database controller ownership changed")
	}
	if object.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return o, nil
	}
	pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{Limit: database.MaxMembers + 1, LabelSelector: "!" + databaseRecoveryHelper})
	if err != nil {
		return o, err
	}
	if len(pods.Items) > database.MaxMembers || pods.Continue != "" {
		return o, fmt.Errorf("database member list exceeds its bound")
	}
	primary, _, _ := unstructured.NestedString(object.Object, "status", "currentPrimary")
	o.Primary = primary
	ready := 0
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			o.Message = "Waiting for replaced database members to stop."
			return o, nil
		}
		// CNPG bootstrap jobs are temporary helpers, not database members.
		if d.Spec.Engine == "postgresql" && pod.Labels["cnpg.io/jobRole"] != "" {
			continue
		}
		if !c.databasePodOwned(ctx, pod, object.GetUID()) {
			return o, fmt.Errorf("database member ownership changed")
		}
		m := database.Member{Name: pod.Name, UID: string(pod.UID), Role: "replica", Node: pod.Spec.NodeName}
		if pod.Name == primary {
			m.Role = "primary"
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				m.Ready = true
			}
		}
		m.Ready = m.Ready && databasePodMatches(pod, d) && databasePodPolicyMatches(pod, policy)
		if m.Ready {
			ready++
		}
		o.Members = append(o.Members, m)
	}
	if len(o.Members) != d.Spec.Members() || ready != d.Spec.Members() {
		o.Message = "Waiting for database members to become ready."
		return o, nil
	}
	if d.Spec.Engine == "postgresql" {
		phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
		if primary == "" || phase != "Cluster in healthy state" {
			o.Message = "Waiting for PostgreSQL primary and replication health."
			return o, nil
		}
		if err = c.observePostgresDatabase(ctx, d, &o); err != nil {
			return o, err
		}
		o.Endpoints = append(o.Endpoints, database.Endpoint{Purpose: "read_write", Host: "database-rw." + ns.Name + ".svc", Port: 5432})
		if d.Spec.Replicas > 0 {
			o.Endpoints = append(o.Endpoints, database.Endpoint{Purpose: "read_only", Host: "database-ro." + ns.Name + ".svc", Port: 5432})
		}
	} else {
		if err = c.observeRedisDatabase(ctx, d, &o); err != nil {
			return o, err
		}
	}
	if err = c.databaseEndpointsReady(ctx, d, o); err != nil {
		return o, err
	}
	o.Status = "ready"
	o.Message = ""
	return o, nil
}
func (c *Client) databasePodOwned(ctx context.Context, p corev1.Pod, uid types.UID) bool {
	for _, owner := range p.OwnerReferences {
		if owner.UID == uid {
			return true
		}
		if owner.Kind == "StatefulSet" {
			s, err := c.kube.AppsV1().StatefulSets(p.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
			if err != nil || s.UID != owner.UID {
				return false
			}
			for _, parent := range s.OwnerReferences {
				if parent.UID == uid {
					return true
				}
			}
		}
	}
	return false
}

// Keep resource ownership checks and the generated spec easy to inspect in tests.
func databaseObjectJSON(d database.Resource) ([]byte, error) {
	o, err := DatabaseObject(d)
	if err != nil {
		return nil, err
	}
	return json.Marshal(o.Object)
}

func overlayDatabaseFields(current, desired map[string]any) {
	for key, value := range desired {
		next, nested := value.(map[string]any)
		old, exists := current[key].(map[string]any)
		if nested && exists {
			overlayDatabaseFields(old, next)
		} else {
			current[key] = value
		}
	}
}
func databasePodMatches(p corev1.Pod, d database.Resource) bool {
	for _, container := range p.Spec.Containers {
		if container.Image != databaseImages[d.Spec.Engine+":"+d.Spec.Version] {
			continue
		}
		for key, expected := range map[corev1.ResourceName]string{corev1.ResourceCPU: d.Spec.CPU, corev1.ResourceMemory: d.Spec.Memory} {
			q := resource.MustParse(expected)
			request, limit := container.Resources.Requests[key], container.Resources.Limits[key]
			if request.Cmp(q) != 0 || limit.Cmp(q) != 0 {
				return false
			}
		}
		return true
	}
	return false
}

// DatabaseRevisionApplied resolves ambiguous retries after an API write: the
// controller annotation and desired fields must both match the accepted revision.
func (c *Client) DatabaseRevisionApplied(ctx context.Context, d database.Resource) (bool, error) {
	gvr, _ := databaseGVR(d.Spec)
	current, err := c.dynamic.Resource(gvr).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current.GetLabels()[databaseOwner] != d.ID || current.GetLabels()[managedBy] != "hakopod" {
		return false, fmt.Errorf("database ownership changed")
	}
	if current.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return false, nil
	}
	desired, err := c.databaseObject(ctx, d)
	if err != nil {
		return false, err
	}
	before := current.DeepCopy()
	overlayDatabaseFields(current.Object["spec"].(map[string]any), desired.Object["spec"].(map[string]any))
	return reflect.DeepEqual(before.Object["spec"], current.Object["spec"]), nil
}
