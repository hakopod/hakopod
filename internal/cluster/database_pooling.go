package cluster

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const databasePoolerLabel = "hakopod.io/database-pooler"
const databasePoolerImage = "ghcr.io/cloudnative-pg/pgbouncer:1.25.1@sha256:e6ddfe22d845e603825e235dd8334b21ecd125abea2a2172478f556b8dee2bb8"

var databasePoolerResource = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "poolers"}

func databasePoolerRoutes(s database.Spec) []string {
	if s.Pooling == nil {
		return nil
	}
	routes := []string{"rw"}
	if s.Pooling.ReadOnly {
		routes = append(routes, "ro")
	}
	return routes
}
func databasePoolerPurpose(route string) string {
	if route == "ro" {
		return "pooled_read_only"
	}
	return "pooled_read_write"
}
func databasePoolerHosts(d database.Resource) []any {
	hosts := []any{}
	for _, route := range databasePoolerRoutes(d.Spec) {
		name := "database-pool-" + route
		for _, suffix := range []string{"", "." + DatabaseNamespace(d.ID), "." + DatabaseNamespace(d.ID) + ".svc", "." + DatabaseNamespace(d.ID) + ".svc.cluster.local"} {
			hosts = append(hosts, name+suffix)
		}
	}
	return hosts
}

func databasePostgresIdentityNames(d database.Resource) []any {
	names := databasePoolerHosts(d)
	seen := map[string]bool{}
	for _, raw := range names {
		seen[raw.(string)] = true
	}
	public := append([]string(nil), d.PublicEndpointNames...)
	slices.Sort(public)
	for _, name := range public {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

func (c *Client) databasePoolerObject(ctx context.Context, d database.Resource, route string, owner types.UID) (*unstructured.Unstructured, error) {
	p := d.Spec.Pooling
	if p == nil {
		return nil, fmt.Errorf("pooling is not configured")
	}
	name, ns := "database-pool-"+route, DatabaseNamespace(d.ID)
	labels := map[string]any{}
	for key, value := range databaseLabels(d) {
		labels[key] = value
	}
	labels[databasePoolerLabel] = route
	resources := map[string]any{"cpu": database.PoolerCPU, "memory": database.PoolerMemory}
	pod := map[string]any{"containers": []any{map[string]any{"name": "pgbouncer", "image": databasePoolerImage, "resources": map[string]any{"requests": resources, "limits": resources}, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "runAsNonRoot": true, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []any{"ALL"}}}}}}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return nil, err
	}
	nodes := d.Spec.Placement.NodeNames
	if policy != nil {
		pod["runtimeClassName"] = policy.RuntimeClass
		pod["nodeSelector"] = map[string]any{"hakopod.com/pool": policy.Pool, DatabaseDefaultRuntimeLabel: policy.RuntimeClass}
		pod["tolerations"] = []any{map[string]any{"key": "hakopod.com/pool", "operator": "Equal", "value": policy.Pool, "effect": "NoSchedule"}}
		if len(nodes) == 0 {
			nodes = policy.nodes()
		}
	}
	affinity := map[string]any{}
	if len(nodes) > 0 {
		terms := []any{}
		for _, node := range nodes {
			terms = append(terms, map[string]any{"matchFields": []any{map[string]any{"key": "metadata.name", "operator": "In", "values": []any{node}}}})
		}
		affinity["nodeAffinity"] = map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{"nodeSelectorTerms": terms}}
	}
	if d.Spec.Placement.Spread != "" {
		affinity["podAntiAffinity"] = map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{"topologyKey": databaseTopologyKey(d.Spec), "labelSelector": map[string]any{"matchLabels": map[string]any{databasePoolerLabel: route}}}}}
	}
	if len(affinity) > 0 {
		pod["affinity"] = affinity
	}
	parameters := map[string]any{"client_tls_sslmode": "require", "server_tls_sslmode": "verify-full", "server_tls_protocols": "tlsv1.2,tlsv1.3", "max_client_conn": strconv.Itoa(p.MaxClientConnections), "default_pool_size": strconv.Itoa(p.DefaultPoolSize), "max_db_connections": strconv.Itoa(p.DefaultPoolSize), "max_user_connections": strconv.Itoa(p.DefaultPoolSize), "query_wait_timeout": "30", "server_connect_timeout": "5", "server_idle_timeout": "10", "server_lifetime": "60", "client_login_timeout": "10", "log_connections": "0", "log_disconnections": "0", "log_pooler_errors": "0", "max_prepared_statements": "100"}
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "postgresql.cnpg.io/v1", "kind": "Pooler", "metadata": map[string]any{"name": name, "namespace": ns, "labels": labels, "annotations": map[string]any{"hakopod.io/database-revision": strconv.FormatInt(d.Revision, 10)}, "ownerReferences": []any{map[string]any{"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster", "name": "database", "uid": string(owner)}}}, "spec": map[string]any{"cluster": map[string]any{"name": "database"}, "type": route, "instances": int64(p.Instances), "pgbouncer": map[string]any{"image": databasePoolerImage, "poolMode": p.Mode, "parameters": parameters, "pg_hba": []any{"hostssl app app all scram-sha-256", "host all all all reject"}}, "template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": pod}}}}
	return object, nil
}

func (c *Client) applyDatabasePoolers(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.Pooling == nil {
		return nil
	}
	cluster, err := c.dynamic.Resource(pgDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || cluster.GetLabels()[databaseOwner] != d.ID || cluster.GetUID() == "" {
		return fmt.Errorf("pooler database ownership is unavailable")
	}
	api := c.dynamic.Resource(databasePoolerResource).Namespace(DatabaseNamespace(d.ID))
	for _, route := range databasePoolerRoutes(d.Spec) {
		object, err := c.databasePoolerObject(ctx, d, route, cluster.GetUID())
		if err != nil {
			return err
		}
		current, err := api.Get(ctx, object.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if err = before(); err != nil {
				return err
			}
			if _, err = api.Create(ctx, object, metav1.CreateOptions{}); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !databasePoolerOwned(current, d, cluster.GetUID(), route) {
			return fmt.Errorf("pooler ownership changed")
		}
		proposed := current.DeepCopy()
		overlayDatabaseFields(proposed.Object["spec"].(map[string]any), object.Object["spec"].(map[string]any))
		annotations := proposed.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["hakopod.io/database-revision"] = strconv.FormatInt(d.Revision, 10)
		proposed.SetAnnotations(annotations)
		if reflect.DeepEqual(proposed.Object, current.Object) {
			continue
		}
		if err = before(); err != nil {
			return err
		}
		if _, err = api.Update(ctx, proposed, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}
func databasePoolerOwned(object *unstructured.Unstructured, d database.Resource, owner types.UID, route string) bool {
	if object.GetDeletionTimestamp() != nil || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" || object.GetLabels()[databasePoolerLabel] != route {
		return false
	}
	for _, ref := range object.GetOwnerReferences() {
		if ref.UID == owner && ref.Kind == "Cluster" && ref.APIVersion == "postgresql.cnpg.io/v1" {
			return true
		}
	}
	return false
}
func (c *Client) databasePoolerPodOwned(ctx context.Context, pod corev1.Pod, owner types.UID) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind != "ReplicaSet" {
			continue
		}
		rs, err := c.kube.AppsV1().ReplicaSets(pod.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil || rs.UID != ref.UID {
			return false
		}
		for _, ref := range rs.OwnerReferences {
			if ref.Kind != "Deployment" {
				continue
			}
			deployment, err := c.kube.AppsV1().Deployments(pod.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil || deployment.UID != ref.UID {
				return false
			}
			for _, ref := range deployment.OwnerReferences {
				if ref.Kind == "Pooler" && ref.APIVersion == "postgresql.cnpg.io/v1" && ref.UID == owner {
					return true
				}
			}
		}
	}
	return false
}

func (c *Client) observeDatabasePoolers(ctx context.Context, d database.Resource, cluster *unstructured.Unstructured, o *database.Observation) error {
	if d.Spec.Pooling == nil {
		return nil
	}
	o.Pooling = &database.PoolingObservation{Members: []database.Member{}}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return err
	}
	pending := false
	for _, route := range databasePoolerRoutes(d.Spec) {
		name := "database-pool-" + route
		object, err := c.dynamic.Resource(databasePoolerResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, name, metav1.GetOptions{})
		if err != nil || !databasePoolerOwned(object, d, cluster.GetUID(), route) {
			return fmt.Errorf("pooler controller ownership is unavailable")
		}
		if object.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
			return fmt.Errorf("pooler configuration revision is pending")
		}
		pods, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{LabelSelector: databasePoolerLabel + "=" + route, Limit: 8, FieldSelector: activeDatabasePodFields})
		if err != nil || pods.Continue != "" || len(pods.Items) > 7 {
			return fmt.Errorf("pooler member inventory is unavailable")
		}
		ready := 0
		memberStart := len(o.Pooling.Members)
		for _, pod := range pods.Items {
			if !c.databasePoolerPodOwned(ctx, pod, object.GetUID()) {
				return fmt.Errorf("pooler member ownership changed")
			}
			if pod.DeletionTimestamp != nil {
				continue
			}
			m := database.Member{Name: pod.Name, UID: string(pod.UID), Role: databasePoolerPurpose(route), Node: pod.Spec.NodeName, Phase: string(pod.Status.Phase)}
			stamp := pod.CreationTimestamp.Time
			m.CreatedAt = &stamp
			matching := false
			for _, container := range pod.Spec.Containers {
				if container.Name == "pgbouncer" && container.Image == databasePoolerImage && container.Resources.Requests.Cpu().String() == database.PoolerCPU && container.Resources.Limits.Cpu().String() == database.PoolerCPU && container.Resources.Requests.Memory().String() == database.PoolerMemory && container.Resources.Limits.Memory().String() == database.PoolerMemory {
					matching = true
					m.Image = container.Image
				}
			}
			for _, status := range pod.Status.ContainerStatuses {
				m.Restarts += status.RestartCount
			}
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					m.Ready = matching && databasePodPolicyMatches(pod, policy) && (len(d.Spec.Placement.NodeNames) == 0 || slices.Contains(d.Spec.Placement.NodeNames, pod.Spec.NodeName))
				}
			}
			if m.Ready {
				ready++
			}
			o.Pooling.Members = append(o.Pooling.Members, m)
		}
		routeMembers := o.Pooling.Members[memberStart:]
		c.observeDatabaseMemberPlacement(ctx, routeMembers)
		placementSpec := d.Spec
		placementSpec.Shards = 1
		placementSpec.Replicas = d.Spec.Pooling.Instances - 1
		if !databasePlacementObservation(placementSpec, routeMembers).Verified {
			pending = true
			continue
		}
		if ready != d.Spec.Pooling.Instances {
			pending = true
			continue
		}
		service, err := c.kube.CoreV1().Services(DatabaseNamespace(d.ID)).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("pooler service is unavailable")
		}
		owned := false
		for _, ref := range service.OwnerReferences {
			if ref.UID == object.GetUID() && ref.Kind == "Pooler" {
				owned = true
			}
		}
		if !owned {
			return fmt.Errorf("pooler service ownership changed")
		}
		o.Endpoints = append(o.Endpoints, database.Endpoint{Purpose: databasePoolerPurpose(route), Host: name + "." + DatabaseNamespace(d.ID) + ".svc", Port: 5432})
	}
	if pending {
		return fmt.Errorf("waiting for the configured pooler instances")
	}
	o.Pooling.Ready = true
	return nil
}
