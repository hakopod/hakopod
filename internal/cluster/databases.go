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
const redisControllerTLSPolicy = "ca-verified-v1"

var pgDatabaseResource = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
var redisDatabaseResource = schema.GroupVersionResource{Group: "redis.redis.opstreelabs.in", Version: "v1beta2", Resource: "redis"}
var redisClusterResource = schema.GroupVersionResource{Group: "redis.redis.opstreelabs.in", Version: "v1beta2", Resource: "redisclusters"}
var mysqlDatabaseResource = schema.GroupVersionResource{Group: "mysql.oracle.com", Version: "v2", Resource: "innodbclusters"}
var databaseImages = map[string]string{
	"postgresql:17":   "ghcr.io/cloudnative-pg/postgresql:17.11@sha256:70664ebcfa1100361b5bdc28bbf06fdbe08db2dc4ad7bd14de33c5e05fe8ea8e",
	"postgresql:18":   "ghcr.io/cloudnative-pg/postgresql:18.6@sha256:899d3ed526b659d77935dde0e6bf2d69dbbf17d3d8c6486ca8cfd04bd3c18533",
	"redis:8":         "ghcr.io/hakopod/hakopod-redis-runtime:8.2.10-opstree@sha256:caa42ea6bb728692da6045aca364f2ae3a33e15242a2e874dfbfa75ecceb5657",
	"mysql:8.4":       "container-registry.oracle.com/mysql/community-server:8.4.12@sha256:7dcc4add9183664de3a214daf85a50c3ba6cccfd7534f700b6561bf5b41885be",
	"mongodb:8.0":     mongodbServerImage,
	"clickhouse:26.3": clickhouseServerImage,
	"oracle:23.26":    database.OracleFreeImage,
}

func DatabaseNamespace(id string) string { return "hdb-" + id }
func databaseGVR(s database.Spec) (schema.GroupVersionResource, string) {
	if s.Engine == "vitess" {
		return vitessDatabaseResource, "VitessCluster"
	}
	if oracleEnterprise(s) {
		return oracleEnterpriseResource, "SingleInstanceDatabase"
	}
	if s.Engine == "oracle" {
		return oracleDatabaseResource, "StatefulSet"
	}
	if s.Engine == "clickhouse" {
		return clickhouseDatabaseResource, "ClickHouseInstallation"
	}
	if s.Engine == "mongodb" {
		return mongodbDatabaseResource, "MongoDBCommunity"
	}
	if s.Engine == "mysql" {
		return mysqlDatabaseResource, "InnoDBCluster"
	}
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
	switch s.Engine {
	case "oracle":
		return fmt.Errorf("Oracle Database is unavailable in this release pending native qualification")
	}
	if c == nil || c.dynamic == nil || c.kube == nil {
		return fmt.Errorf("database controller is unavailable")
	}
	if s.Engine == "vitess" {
		if err := vitessRuntimeSupported(s); err != nil {
			return err
		}
		if c.options.VitessBackup == nil {
			return fmt.Errorf("Vitess native recovery storage is not configured")
		}
		_, err := c.dynamic.Resource(vitessDatabaseResource).Namespace("default").List(ctx, metav1.ListOptions{Limit: 1})
		if err != nil {
			return fmt.Errorf("Vitess controller APIs are unavailable")
		}
		return nil
	}
	if oracleEnterprise(s) {
		return c.oracleEnterpriseControllerAvailable(ctx)
	}
	if s.Engine == "oracle" {
		return oracleRuntimeSupported(s)
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
	if s.Engine == "mysql" {
		namespace, name = "mysql-operator", "mysql-operator"
	}
	if s.Engine == "mongodb" {
		namespace, name = "mongodb-system", "mongodb-kubernetes-operator"
	}
	if s.Engine == "clickhouse" {
		namespace, name = "clickhouse-operator", "clickhouse-operator"
	}
	deployment, err := c.kube.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil || deployment.DeletionTimestamp != nil || deployment.Status.ObservedGeneration < deployment.Generation || deployment.Status.AvailableReplicas < 1 || deployment.Status.UpdatedReplicas < 1 || deployment.Status.UpdatedReplicas != deployment.Status.Replicas || deployment.Status.AvailableReplicas < deployment.Status.UpdatedReplicas || deployment.Status.UnavailableReplicas > 0 {
		return fmt.Errorf("the database controller has no available current deployment")
	}
	if s.Engine == "redis" && !safeRedisController(deployment.Spec.Template) {
		return fmt.Errorf("the Redis controller requires the verified credential-safe build and a bounded 20-minute command timeout")
	}
	if s.Engine == "mysql" && !safeMySQLController(deployment.Spec.Template) {
		return fmt.Errorf("MySQL requires the pinned controller with bounded resources and credential-safe logging")
	}
	if s.Engine == "mongodb" && !safeMongoDBController(deployment.Spec.Template) {
		return fmt.Errorf("MongoDB requires the pinned Community controller with bounded resources and telemetry disabled")
	}
	if s.Engine == "clickhouse" && !safeClickHouseController(deployment.Spec.Template) {
		return fmt.Errorf("ClickHouse requires the pinned controller with strict TLS and bounded resources")
	}
	if s.Engine == "redis" && s.TLSRequired() && !safeRedisTLSController(deployment.Spec.Template) {
		return fmt.Errorf("the Redis controller requires the verified TLS build and configuration generator")
	}
	if s.Pooling != nil {
		if _, err = c.dynamic.Resource(databasePoolerResource).Namespace("default").List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
			return fmt.Errorf("the PostgreSQL pooling controller is unavailable")
		}
	}
	return nil
}

func safeRedisTLSController(pod corev1.PodTemplateSpec) bool {
	if pod.Annotations["hakopod.io/redis-tls-policy"] != redisControllerTLSPolicy {
		return false
	}
	for _, container := range pod.Spec.Containers {
		if container.Name != "redis-operator" {
			continue
		}
		config, initImage := "", ""
		for _, env := range container.Env {
			if env.Name == "FEATURE_GATES" && env.ValueFrom == nil {
				config = env.Value
			}
			if env.Name == "INIT_CONTAINER_IMAGE" && env.ValueFrom == nil {
				initImage = env.Value
			}
		}
		if initImage != container.Image {
			return false
		}
		enabled := false
		for _, gate := range strings.Split(config, ",") {
			if strings.HasPrefix(gate, "GenerateConfigInInitContainer=") {
				enabled = gate == "GenerateConfigInInitContainer=true"
			}
		}
		return enabled
	}
	return false
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
	if oracleEnterprise(d.Spec) {
		// Rendering a review does not admit an unqualified runtime.
		registry := ""
		if d.Spec.Oracle.RegistryCredential != "" {
			registry = "hp-registry-" + RegistryScope(d.Project, d.Environment, d.Spec.Oracle.RegistryCredential)[:20]
		}
		return oracleEnterpriseObject(d, 0, registry, nil, d.Spec.Placement.NodeNames), nil
	}
	gvr, kind := databaseGVR(d.Spec)
	image, ok := databaseImages[d.Spec.Engine+":"+d.Spec.Version]
	if d.Spec.Engine == "vitess" {
		image, ok = vitessServerImage, true
	}
	if !ok {
		return nil, fmt.Errorf("no verified database image for this version")
	}
	resources := map[string]any{"requests": map[string]any{"cpu": d.Spec.CPU, "memory": d.Spec.Memory}, "limits": map[string]any{"cpu": d.Spec.CPU, "memory": d.Spec.Memory}}
	var spec map[string]any
	if d.Spec.Engine == "postgresql" {
		spec = map[string]any{"instances": int64(d.Spec.Members()), "imageName": image, "enableSuperuserAccess": false, "resources": resources, "storage": map[string]any{"size": fmt.Sprintf("%dGi", d.Spec.StorageGiB)}, "bootstrap": map[string]any{"initdb": map[string]any{"database": "app", "owner": "app", "secret": map[string]any{"name": "database-credentials"}}}}
		if d.Spec.TLSRequired() {
			spec["postgresql"] = map[string]any{"parameters": map[string]any{"ssl_min_protocol_version": "TLSv1.2"}, "pg_hba": []any{"hostnossl all all all reject", "hostssl all all all scram-sha-256"}}
		}
		if d.Spec.Pooling != nil || len(d.PublicEndpointNames) > 0 {
			spec["certificates"] = map[string]any{"serverAltDNSNames": databasePostgresIdentityNames(d)}
		}
	} else if d.Spec.Engine == "vitess" {
		spec = vitessDatabaseSpec(d, resources)
	} else if d.Spec.Engine == "mysql" {
		spec = mysqlDatabaseSpec(d, resources)
	} else if d.Spec.Engine == "mongodb" {
		spec = mongodbDatabaseSpec(d, resources)
	} else if d.Spec.Engine == "clickhouse" {
		spec = clickhouseDatabaseSpec(d, resources)
	} else if d.Spec.Engine == "oracle" {
		if err := oracleRuntimeSupported(d.Spec); err != nil {
			return nil, err
		}
		spec = oracleDatabaseSpec(d, resources)
	} else {
		spec = map[string]any{"kubernetesConfig": map[string]any{"image": image, "imagePullPolicy": "IfNotPresent", "resources": resources, "redisSecret": map[string]any{"name": "database-credentials", "key": "password"}}, "podSecurityContext": map[string]any{"runAsUser": int64(1000), "fsGroup": int64(1000)}, "storage": map[string]any{"volumeClaimTemplate": map[string]any{"spec": map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", d.Spec.StorageGiB)}}}}}}
		if d.Spec.TLSRequired() {
			spec["TLS"] = map[string]any{"ca": "ca.crt", "cert": "tls.crt", "key": "tls.key", "secret": map[string]any{"secretName": "database-tls"}}
		}
		if d.Spec.Mode == "standalone" {
			spec["securityContext"] = map[string]any{"runAsUser": int64(1000), "runAsNonRoot": true, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}
			if d.Spec.TLSRequired() {
				spec["redisConfig"] = map[string]any{"additionalRedisConfig": "database-security"}
			}
		}
		if d.Spec.Mode == "cluster" {
			// Redis node identity and slot ownership must survive a pod replacement.
			// The image's default /node-conf directory is outside its persistent volume.
			spec["env"] = []any{map[string]any{"name": "NODE_CONF_DIR", "value": "/data/node-conf"}}
			if d.Spec.TLSRequired() {
				spec["env"].([]any)[0].(map[string]any)["value"] = "/data"
				spec["env"] = append(spec["env"].([]any), map[string]any{"name": "HAKOPOD_REDIS_DNS_SUFFIX", "value": "." + DatabaseNamespace(d.ID) + ".svc"})
			}
			spec["clusterSize"] = int64(d.Spec.Shards)
			spec["clusterVersion"] = "v7"
			spec["persistenceEnabled"] = true
			spec["redisLeader"] = map[string]any{"replicas": int64(d.Spec.Shards)}
			spec["redisFollower"] = map[string]any{"replicas": int64(d.Spec.Shards * d.Spec.Replicas)}
			if d.Spec.TLSRequired() {
				// Cluster configuration is role-scoped in the operator CRD. A
				// top-level redisConfig is silently pruned by Kubernetes.
				for _, role := range []string{"redisLeader", "redisFollower"} {
					spec[role].(map[string]any)["redisConfig"] = map[string]any{"additionalRedisConfig": "database-security"}
				}
			}
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
	if err := c.DatabaseControllerAvailable(ctx, d.Spec); err != nil {
		return err
	}
	if len(password) < 32 || len(password) > 128 {
		return fmt.Errorf("database credentials are unavailable")
	}
	if oracleEnterprise(d.Spec) {
		return c.applyOracleEnterpriseDatabase(ctx, d, password, before)
	}
	if d.Spec.Engine == "vitess" {
		if err := c.ReconcileVitessBackupAuthority(ctx, d, before); err != nil {
			return err
		}
	}
	object, err := c.databaseObject(ctx, d)
	if err != nil {
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
		metadata := metav1.ObjectMeta{Name: "database-credentials", Namespace: ns, Labels: databaseLabels(d)}
		if d.Spec.Engine == "vitess" {
			if existing.UID == "" || existing.DeletionTimestamp != nil {
				return fmt.Errorf("Vitess credential namespace identity changed")
			}
			metadata = databaseIdentityMeta(d, existing.UID, "database-credentials")
		}
		secret, err = c.kube.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{ObjectMeta: metadata, Type: corev1.SecretTypeBasicAuth, Immutable: ptr(true), Data: map[string][]byte{"username": []byte("app"), "password": password}}, metav1.CreateOptions{})
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
	if d.Spec.Engine == "vitess" {
		if err = c.reconcileVitessCredentialOwnership(ctx, d, existing, secret, password, before); err != nil {
			return err
		}
	}
	if err = c.databaseNetworkPolicy(ctx, d, before); err != nil {
		return err
	}
	if err = c.prepareDatabaseIdentity(ctx, d, before); err != nil {
		return err
	}
	if d.Spec.Engine == "clickhouse" {
		if err = c.prepareClickHouseClientIdentity(ctx, d, before); err != nil {
			return err
		}
	}
	if err = c.prepareRedisSecurity(ctx, d, before); err != nil {
		return err
	}
	if err = c.prepareMySQLSecurity(ctx, d, before); err != nil {
		return err
	}
	if err = c.prepareMongoDBSecurity(ctx, d, before); err != nil {
		return err
	}
	if err = c.prepareClickHouseSecurity(ctx, d, before); err != nil {
		return err
	}
	if err = c.prepareOracleSecurity(ctx, d, before); err != nil {
		return err
	}
	if d.Spec.Engine == "vitess" {
		if err = c.prepareVitessSecurity(ctx, d, password, before); err != nil {
			return err
		}
		storage, e := c.vitessBackupStorage(ctx, d)
		if e != nil {
			return e
		}
		if err = c.prepareVitessBackupStorage(ctx, d, storage, before); err != nil {
			return err
		}
		if err = c.prepareVitessController(ctx, d, before); err != nil {
			return err
		}
		if err = c.applyVitessIdentity(ctx, d, object); err != nil {
			return err
		}
	}
	if d.Spec.Engine == "oracle" {
		identity, e := c.kube.CoreV1().Secrets(ns).Get(ctx, "database-tls", metav1.GetOptions{})
		if e != nil {
			return e
		}
		_ = unstructured.SetNestedField(object.Object, oracleIdentityFingerprint(identity), "spec", "template", "metadata", "annotations", "hakopod.io/oracle-identity")
	}
	gvr, _ := databaseGVR(d.Spec)
	api := c.dynamic.Resource(gvr).Namespace(ns)
	current, err := api.Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = api.Create(ctx, object, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		return c.applyDatabaseAttachments(ctx, d, before)
	}
	if err != nil {
		return fmt.Errorf("database controller state is unavailable")
	}
	if current.GetLabels()[databaseOwner] != d.ID || current.GetLabels()[managedBy] != "hakopod" {
		return fmt.Errorf("database controller object has a different owner")
	}
	if current.GetAnnotations()["hakopod.io/database-revision"] == strconv.FormatInt(d.Revision, 10) {
		if err = c.prepareMySQLAccount(ctx, d, password, before); err != nil {
			return err
		}
		return c.applyDatabaseAttachments(ctx, d, before)
	}
	// Preserve controller finalizers, owner references, defaults and status. Only
	// overlay the fields owned by Hakopod on the latest resource version.
	updated := current.DeepCopy()
	overlayDatabaseFields(updated.Object["spec"].(map[string]any), object.Object["spec"].(map[string]any))
	if d.Spec.Engine == "clickhouse" {
		// User grants are an authoritative policy. Overlaying a removed grant
		// would retain privileges from an earlier accepted configuration.
		updated.Object["spec"].(map[string]any)["configuration"] = object.Object["spec"].(map[string]any)["configuration"]
	}
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
	if err != nil {
		return err
	}
	return c.applyDatabaseAttachments(ctx, d, before)
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
	if d.Spec.Engine == "postgresql" {
		removed, err := c.deletePostgresController(ctx, d, before)
		if err != nil || !removed {
			return false, err
		}
	}
	if d.Spec.Engine == "mysql" && ns.DeletionTimestamp == nil {
		removed, err := c.deleteMySQLController(ctx, d, before)
		if err != nil || !removed {
			return false, err
		}
	}
	if d.Spec.Engine == "clickhouse" && ns.DeletionTimestamp == nil {
		removed, err := c.deleteClickHouseController(ctx, d, before)
		if err != nil || !removed {
			return false, err
		}
	}
	if d.Spec.Engine == "vitess" && ns.DeletionTimestamp == nil {
		removed, err := c.deleteVitessController(ctx, d, before)
		if err != nil || !removed {
			return false, err
		}
	}
	if ns.DeletionTimestamp != nil {
		return false, nil
	}
	if err = c.prepareDatabaseVolumeDeletion(ctx, ns.Name, before); err != nil {
		return false, err
	}
	if err = before(); err != nil {
		return false, err
	}
	err = c.kube.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: ptr(ns.UID)}})
	return false, err
}

// The accepted delete explicitly removes this database's data. Change only
// PVs bound to the exact claims in its owned namespace, never the shared class.
func (c *Client) prepareDatabaseVolumeDeletion(ctx context.Context, namespace string, before func() error) error {
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{Limit: 1025})
	if err != nil {
		return err
	}
	if len(claims.Items) > 1024 || claims.Continue != "" {
		return fmt.Errorf("database volume claim list exceeds its bound")
	}
	volumes := make([]*corev1.PersistentVolume, 0, len(claims.Items))
	for _, claim := range claims.Items {
		if claim.Spec.VolumeName == "" {
			continue
		}
		volume, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		ref := volume.Spec.ClaimRef
		if ref == nil || ref.Namespace != namespace || ref.Name != claim.Name || ref.UID != claim.UID || claim.UID == "" {
			return fmt.Errorf("database volume claim identity changed")
		}
		if volume.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
			volumes = append(volumes, volume)
		}
	}
	for _, volume := range volumes {
		volume.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
		if err = before(); err != nil {
			return err
		}
		if _, err = c.kube.CoreV1().PersistentVolumes().Update(ctx, volume, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) ObserveDatabase(ctx context.Context, d database.Resource) (result database.Observation, err error) {
	if oracleEnterprise(d.Spec) {
		return c.observeOracleEnterpriseDatabase(ctx, d)
	}
	defer func() {
		if ctx.Err() != nil {
			result.Message = "Database observation did not complete within its request lifetime."
			err = fmt.Errorf("database observation interrupted: %w", ctx.Err())
		}
	}()
	o := database.Observation{ObservedAt: time.Now().UTC(), Revision: d.Revision, Status: "pending", Members: []database.Member{}, Endpoints: []database.Endpoint{}}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return o, err
	}
	var clickhouseSandbox string
	if d.Spec.Engine == "clickhouse" {
		clickhouseSandbox, err = c.clickhouseRuntime(ctx, d, policy)
		if err != nil {
			return o, err
		}
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
	vitessIdentity := ""
	if d.Spec.Engine == "vitess" {
		vitessIdentity, err = c.vitessIdentityFingerprint(ctx, d)
		if err != nil {
			return o, err
		}
	}
	memberSelector := "!" + databaseRecoveryHelper + ",!" + databasePoolerLabel + ",!" + databaseRouterLabel + ",!" + databaseKeeperLabel
	if d.Spec.Engine == "vitess" {
		memberSelector = vitessComponentLabel + "=tablet"
	}
	pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{Limit: database.MaxMembers + 1, LabelSelector: memberSelector, FieldSelector: activeDatabasePodFields})
	if err != nil {
		return o, err
	}
	if len(pods.Items) > database.MaxMembers || pods.Continue != "" {
		return o, fmt.Errorf("database member list exceeds its bound")
	}
	primary, _, _ := unstructured.NestedString(object.Object, "status", "currentPrimary")
	o.Primary = primary
	ready := 0
	memberPods := []corev1.Pod{}
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
		m := database.Member{Name: pod.Name, UID: string(pod.UID), Role: "unknown", Node: pod.Spec.NodeName, Phase: string(pod.Status.Phase)}
		if d.Spec.Engine == "vitess" {
			parts := strings.Split(pod.Labels["planetscale.com/shard"], "-")
			if len(parts) != 2 {
				return o, fmt.Errorf("Vitess shard label is invalid")
			}
			if parts[0] == "x" {
				parts[0] = ""
			}
			if parts[1] == "x" {
				parts[1] = ""
			}
			m.Shard = strings.Join(parts, "-")
			m.Image = vitessServerImage
		}
		if !pod.CreationTimestamp.IsZero() {
			stamp := pod.CreationTimestamp.Time
			m.CreatedAt = &stamp
		}
		for _, container := range pod.Spec.Containers {
			if container.Image == databaseImages[d.Spec.Engine+":"+d.Spec.Version] {
				m.Image = container.Image
			}
		}
		for _, status := range pod.Status.ContainerStatuses {
			m.Restarts += status.RestartCount
		}
		memberPods = append(memberPods, pod)
		if d.Spec.Engine == "postgresql" && primary != "" {
			m.Role = "replica"
		}
		if pod.Name == primary {
			m.Role = "primary"
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				m.Ready = true
			}
		}
		m.Ready = m.Ready && databasePodMatches(pod, d) && databasePodPolicyMatches(pod, policy)
		if d.Spec.Engine == "vitess" {
			m.Ready = m.Ready && vitessPodIdentityMatches(pod, vitessIdentity)
		}
		if d.Spec.Engine == "clickhouse" {
			m.Ready = m.Ready && clickhousePodRuntimeMatches(pod, clickhouseSandbox)
		}
		if m.Ready {
			ready++
		}
		o.Members = append(o.Members, m)
	}
	c.observeDatabasePlacement(ctx, d, &o)
	c.observeDatabaseMetrics(ctx, d, memberPods, &o)
	if len(o.Members) != d.Spec.Members() || ready != d.Spec.Members() {
		o.Message = "Waiting for database members to become ready."
		return o, nil
	}
	if (d.Spec.Placement.Spread != "" || len(d.Spec.Placement.NodeNames) > 0) && o.Placement != nil && !o.Placement.Verified {
		o.Message = o.Placement.Message
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
	} else if d.Spec.Engine == "vitess" {
		if err = c.observeVitessDatabase(ctx, d, object, &o, vitessIdentity); err != nil {
			return o, err
		}
	} else if d.Spec.Engine == "mysql" {
		if err = c.observeMySQLDatabase(ctx, d, object, &o); err != nil {
			return o, err
		}
	} else if d.Spec.Engine == "mongodb" {
		if err = c.observeMongoDBDatabase(ctx, d, object, &o); err != nil {
			return o, err
		}
	} else if d.Spec.Engine == "clickhouse" {
		if err = c.observeClickHouseDatabase(ctx, d, object, &o); err != nil {
			return o, err
		}
	} else if d.Spec.Engine == "oracle" {
		if err = c.observeOracleDatabase(ctx, d, object, &o); err != nil {
			return o, err
		}
	} else {
		if err = c.observeRedisDatabase(ctx, d, &o); err != nil {
			return o, err
		}
	}
	if err = c.observeDatabasePoolers(ctx, d, object, &o); err != nil {
		if o.Pooling != nil {
			o.Pooling.Message = err.Error()
		}
		return o, err
	}
	if err = c.databaseEndpointsReady(ctx, d, o); err != nil {
		return o, err
	}
	if err = c.observeDatabaseTLS(ctx, d, &o); err != nil {
		o.Message = "Database TLS enforcement could not be verified."
		return o, err
	}
	o.Status = "ready"
	o.Message = ""
	c.observeDatabaseEngineMetrics(ctx, d, &o)
	return o, nil
}
func (c *Client) databasePodOwned(ctx context.Context, p corev1.Pod, uid types.UID) bool {
	if p.Labels[vitessComponentLabel] != "" {
		return c.vitessPodOwned(ctx, p, uid)
	}
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
	if d.Spec.Engine == "vitess" {
		return vitessPodMatches(p, d)
	}
	if d.Spec.Engine == "clickhouse" && !clickhouseClientIdentityPodMatches(p) {
		return false
	}
	if d.Spec.Engine == "mongodb" && !mongodbPodImagesMatch(p, d) {
		return false
	}
	if d.Spec.Engine == "mysql" && !mysqlPodImagesMatch(p, d) {
		return false
	}
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
	if oracleEnterprise(d.Spec) {
		return c.oracleEnterpriseRevisionApplied(ctx, d)
	}
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
	if d.Spec.Engine == "clickhouse" {
		current.Object["spec"].(map[string]any)["configuration"] = desired.Object["spec"].(map[string]any)["configuration"]
	}
	return reflect.DeepEqual(before.Object["spec"], current.Object["spec"]), nil
}
