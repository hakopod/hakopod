package cluster

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var clickhouseDatabaseResource = schema.GroupVersionResource{Group: "clickhouse.altinity.com", Version: "v1", Resource: "clickhouseinstallations"}

const clickhouseServerImage = "docker.io/clickhouse/clickhouse-server:26.3.33.24@sha256:810861a2e2d0188744f5f23b2d3ec9ff95812bcb9ddbb8fed13a377a7f305893"
const clickhouseControllerImage = "docker.io/altinity/clickhouse-operator:0.27.4@sha256:c60c872fedd85017f843167dfdd2b900bb06b668a3deaf02b21782e59af859e3"
const clickhouseKeeperImage = "docker.io/clickhouse/clickhouse-keeper:26.3.33.24@sha256:3fd59d9efb8c9e9136c3c924ceaa004f65c0b14699f34e4eae4eb63e2f860803"
const databaseKeeperLabel = "hakopod.io/database-keeper"

// Finish controller finalization while its namespace and credentials still
// exist. Namespace deletion races the controller's in-flight reconciliations.
func (c *Client) deleteClickHouseController(ctx context.Context, d database.Resource, before func() error) (bool, error) {
	api := c.dynamic.Resource(clickhouseDatabaseResource).Namespace(DatabaseNamespace(d.ID))
	object, err := api.Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if object.GetUID() == "" || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" {
		return false, fmt.Errorf("ClickHouse controller ownership changed")
	}
	if object.GetDeletionTimestamp() == nil {
		uid := object.GetUID()
		if err = before(); err != nil {
			return false, err
		}
		err = api.Delete(ctx, object.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	}
	return false, err
}

// These commands never interpolate credentials into arguments or server logs.
// The bootstrap account is reachable only over loopback and is not an app binding.
const clickhouseStart = `set -eu
umask 077
clickhouse-server --config-file=/etc/clickhouse-server/config.xml & server=$!
trap 'kill -TERM "$server" 2>/dev/null || true; wait "$server" 2>/dev/null || true' EXIT HUP INT TERM
for attempt in $(seq 1 180); do
  if clickhouse-client --config-file=/etc/hakopod/bootstrap.xml --query "SELECT 1" >/dev/null 2>&1; then
    clickhouse-client --config-file=/etc/hakopod/bootstrap.xml --query "$HAKOPOD_INIT_DATABASE" >/dev/null 2>&1
    touch /tmp/hakopod-initialized
    wait "$server"
    exit $?
  fi
  kill -0 "$server"
  sleep 1
done
exit 1`

func clickhouseDatabaseSpec(d database.Resource, resources map[string]any) map[string]any {
	secret := func(key string) any {
		return map[string]any{"valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "database-clickhouse-access", "key": key}}}
	}
	users := map[string]any{}
	for _, name := range []string{"app", "hakopod-bootstrap", "hakopod-monitor", "hakopod-recovery", "default"} {
		users[name+"/password_sha256_hex"] = secret(name + "-hash")
		users[name+"/networks/ip"] = "127.0.0.1"
		users[name+"/profile"] = "managed"
		users[name+"/quota"] = "default"
	}
	users["app/networks/ip"] = "::/0"
	users["app/grants/query"] = []any{"GRANT SELECT, INSERT, ALTER, CREATE TABLE, CREATE VIEW, DROP TABLE, DROP VIEW, TRUNCATE, OPTIMIZE, SHOW ON app.*"}
	users["hakopod-bootstrap/grants/query"] = []any{"GRANT CREATE DATABASE ON app.*"}
	users["hakopod-monitor/grants/query"] = []any{"GRANT SELECT ON system.*", "GRANT SHOW ON app.*"}
	users["hakopod-recovery/grants/query"] = []any{"GRANT SELECT, INSERT, ALTER, CREATE TABLE, CREATE VIEW, DROP TABLE, DROP VIEW, TRUNCATE, OPTIMIZE, SHOW, BACKUP ON app.*"}
	// Explicit engine grants avoid the legacy compatibility rule that revokes
	// Distributed unless the user has broad remote-source permissions.
	engines := []string{"MergeTree", "ReplacingMergeTree", "SummingMergeTree", "AggregatingMergeTree", "CollapsingMergeTree", "VersionedCollapsingMergeTree", "CoalescingMergeTree", "Memory", "Log", "TinyLog", "StripeLog", "Null", "View", "MaterializedView"}
	if d.Spec.Mode == "cluster" {
		for _, engine := range engines[:7] {
			engines = append(engines, "Replicated"+engine)
		}
		engines = append(engines, "Distributed")
	}
	for _, role := range []string{"app", "hakopod-recovery"} {
		for _, engine := range engines {
			users[role+"/grants/query"] = append(users[role+"/grants/query"].([]any), "GRANT TABLE ENGINE ON "+engine)
		}
	}
	// The image's default user has legacy access settings incompatible with
	// explicit grants. Its random password is discarded and it is loopback-only.
	q := resource.MustParse(d.Spec.Memory)
	profiles := map[string]any{"managed/max_threads": int64(2), "managed/max_insert_threads": int64(1), "managed/max_memory_usage": q.Value() / 4, "managed/max_execution_time": int64(300), "managed/max_concurrent_queries_for_user": int64(32), "managed/log_queries": int64(0), "managed/log_query_threads": int64(0), "managed/allow_introspection_functions": int64(0), "managed/allow_ddl": int64(1)}
	init := "CREATE DATABASE IF NOT EXISTS app ENGINE=Atomic"
	users["hakopod-bootstrap/grants/query"] = append(users["hakopod-bootstrap/grants/query"].([]any), "GRANT TABLE ENGINE ON Atomic")
	if d.Spec.Mode == "cluster" {
		init = "CREATE DATABASE IF NOT EXISTS app ENGINE=Replicated('/hakopod/" + d.ID + "/database/app', '{shard}', '{replica}')"
		users["hakopod-bootstrap/grants/query"] = []any{"GRANT CREATE DATABASE ON app.*", "GRANT TABLE ENGINE ON Replicated"}
		profiles["managed/default_table_engine"] = "ReplicatedMergeTree"
	}
	security := map[string]any{"runAsNonRoot": true, "runAsUser": int64(101), "runAsGroup": int64(101), "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []any{"ALL"}}}
	pod := map[string]any{
		"automountServiceAccountToken":  false,
		"securityContext":               map[string]any{"runAsNonRoot": true, "runAsUser": int64(101), "runAsGroup": int64(101), "fsGroup": int64(101), "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
		"terminationGracePeriodSeconds": int64(90),
		"containers": []any{map[string]any{
			"name": "clickhouse", "image": clickhouseServerImage, "imagePullPolicy": "IfNotPresent", "resources": resources, "securityContext": security,
			"command":        []any{"bash", "-c", clickhouseStart},
			"env":            []any{map[string]any{"name": "HAKOPOD_INIT_DATABASE", "value": init}, map[string]any{"name": "HAKOPOD_INTERSERVER_PASSWORD", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "database-clickhouse-access", "key": "interserver"}}}},
			"volumeMounts":   []any{map[string]any{"name": clickhouseClientTLSSecret, "mountPath": "/etc/hakopod-client-tls", "readOnly": true}, map[string]any{"name": "database-tls", "mountPath": "/etc/hakopod-tls", "readOnly": true}, map[string]any{"name": "database-access", "mountPath": "/etc/hakopod", "readOnly": true}, map[string]any{"name": "temporary", "mountPath": "/tmp"}, map[string]any{"name": "logs", "mountPath": "/var/log/clickhouse-server"}, map[string]any{"name": "backup", "mountPath": "/var/lib/clickhouse/backups"}},
			"readinessProbe": map[string]any{"exec": map[string]any{"command": []any{"bash", "-c", "test -f /tmp/hakopod-initialized && clickhouse-client --config-file=/etc/hakopod/monitor.xml --query 'SELECT 1' >/dev/null 2>&1"}}, "timeoutSeconds": int64(5), "periodSeconds": int64(10)},
			"livenessProbe":  map[string]any{"tcpSocket": map[string]any{"port": int64(9440)}, "timeoutSeconds": int64(3), "periodSeconds": int64(15)},
			"startupProbe":   map[string]any{"tcpSocket": map[string]any{"port": int64(9440)}, "periodSeconds": int64(5), "failureThreshold": int64(60)},
		}},
		"volumes": []any{map[string]any{"name": clickhouseClientTLSSecret, "secret": map[string]any{"secretName": clickhouseClientTLSSecret, "defaultMode": int64(0440)}}, map[string]any{"name": "database-tls", "secret": map[string]any{"secretName": "database-tls", "defaultMode": int64(0440)}}, map[string]any{"name": "database-access", "secret": map[string]any{"secretName": "database-clickhouse-access", "defaultMode": int64(0440)}}, map[string]any{"name": "temporary", "emptyDir": map[string]any{"sizeLimit": "128Mi"}}, map[string]any{"name": "logs", "emptyDir": map[string]any{"sizeLimit": "64Mi"}}},
	}
	cluster := map[string]any{"name": "managed", "secure": "yes", "insecure": "no", "layout": map[string]any{"shardsCount": int64(d.Spec.Shards), "replicasCount": int64(d.Spec.Replicas + 1)}}
	config := map[string]any{"clusters": []any{cluster}, "users": users, "profiles": profiles, "files": map[string]any{"zz-hakopod.xml": clickhouseServerConfiguration(d)}}
	if d.Spec.Mode == "cluster" {
		nodes := []any{}
		for i := 0; i < d.Spec.KeeperInstances(); i++ {
			nodes = append(nodes, map[string]any{"host": clickhouseKeeperHost(d, i), "port": int64(9281), "secure": "yes"})
		}
		config["zookeeper"] = map[string]any{"nodes": nodes, "session_timeout_ms": int64(30000), "operation_timeout_ms": int64(10000)}
	}
	ports := []any{map[string]any{"name": "https", "port": int64(8443), "targetPort": int64(8443)}, map[string]any{"name": "tcp-secure", "port": int64(9440), "targetPort": int64(9440)}}
	return map[string]any{
		"taskID":                 fmt.Sprintf("revision-%d", d.Revision),
		"namespaceDomainPattern": "%s.svc.cluster.local",
		"security":               map[string]any{"clickhouse": map[string]any{"tls": map[string]any{"verify": "Strict", "minVersion": "1.2", "rootCASecretRef": map[string]any{"name": "database-tls", "key": "ca.crt"}}}, "zookeeper": map[string]any{"tls": map[string]any{"verify": "Strict", "minVersion": "1.2"}}},
		"reconcile":              map[string]any{"runtime": map[string]any{"reconcileShardsThreadsNumber": int64(1)}, "statefulSet": map[string]any{"create": map[string]any{"onFailure": "abort"}, "update": map[string]any{"onFailure": "abort", "timeout": int64(300)}, "recreate": map[string]any{"onDataLoss": "abort", "onUpdateFailure": "abort"}}},
		"defaults":               map[string]any{"replicasUseFQDN": "yes", "storageManagement": map[string]any{"provisioner": "Operator", "reclaimPolicy": "Retain"}, "templates": map[string]any{"podTemplate": "managed", "dataVolumeClaimTemplate": "data", "serviceTemplate": "client"}},
		"configuration":          config,
		"templates": map[string]any{
			"podTemplates":         []any{map[string]any{"name": "managed", "metadata": map[string]any{"labels": map[string]any{databaseOwner: d.ID}}, "spec": pod}},
			"serviceTemplates":     []any{map[string]any{"name": "client", "generateName": "database", "spec": map[string]any{"type": "ClusterIP", "ports": ports}}},
			"volumeClaimTemplates": []any{map[string]any{"name": "data", "reclaimPolicy": "Retain", "spec": map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", d.Spec.StorageGiB)}}}}, map[string]any{"name": "backup", "reclaimPolicy": "Retain", "spec": map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", d.Spec.StorageGiB)}}}}},
		},
	}
}

func clickhouseServerConfiguration(d database.Resource) string {
	q := resource.MustParse(d.Spec.Memory)
	clusterAccess := ""
	if d.Spec.Mode == "cluster" {
		// Authenticate inter-shard queries as their originating application user.
		// This secret stays on database members and never enters application URLs.
		// The operator initially omits unready hosts from its generated cluster.
		// A secret-only cluster cannot start, so publish the complete owned TLS
		// topology from the accepted spec, including members still bootstrapping.
		var topology strings.Builder
		topology.WriteString(`<remote_servers><managed replace="1"><secret from_env="HAKOPOD_INTERSERVER_PASSWORD"/>`)
		for shard := 0; shard < d.Spec.Shards; shard++ {
			topology.WriteString(`<shard><internal_replication>true</internal_replication>`)
			for replica := 0; replica <= d.Spec.Replicas; replica++ {
				fmt.Fprintf(&topology, `<replica><host>chi-database-managed-%d-%d.%s.svc.cluster.local</host><port>9440</port><secure>1</secure></replica>`, shard, replica, DatabaseNamespace(d.ID))
			}
			topology.WriteString(`</shard>`)
		}
		topology.WriteString(`</managed></remote_servers>`)
		clusterAccess = topology.String()
	}
	return fmt.Sprintf(`<clickhouse>
%s
<access_control_improvements><table_engines_require_grant>true</table_engines_require_grant></access_control_improvements>
<http_port remove="1"/><tcp_port remove="1"/><interserver_http_port remove="1"/>
<https_port>8443</https_port><tcp_port_secure>9440</tcp_port_secure><interserver_https_port>9010</interserver_https_port>
<protocols><management><type>tcp</type><host>127.0.0.1</host><port>9000</port></management></protocols>
<listen_host replace="1">0.0.0.0</listen_host><listen_try>0</listen_try>
<openSSL replace="1"><server><certificateFile>/etc/hakopod-client-tls/tls.crt</certificateFile><privateKeyFile>/etc/hakopod-client-tls/tls.key</privateKeyFile><caConfig>/etc/hakopod-tls/ca.crt</caConfig><verificationMode>none</verificationMode><disableProtocols>sslv2,sslv3,tlsv1,tlsv1_1</disableProtocols></server><client><certificateFile>/etc/hakopod-tls/tls.crt</certificateFile><privateKeyFile>/etc/hakopod-tls/tls.key</privateKeyFile><caConfig>/etc/hakopod-tls/ca.crt</caConfig><verificationMode>strict</verificationMode><loadDefaultCAFile>false</loadDefaultCAFile><extendedVerification>true</extendedVerification><disableProtocols>sslv2,sslv3,tlsv1,tlsv1_1</disableProtocols><invalidCertificateHandler><name>RejectCertificateHandler</name></invalidCertificateHandler></client></openSSL>
<interserver_http_credentials><user>replication</user><password from_env="HAKOPOD_INTERSERVER_PASSWORD"/><allow_empty>false</allow_empty></interserver_http_credentials>
<default_replica_path>/hakopod/%s/tables/{shard}/{uuid}</default_replica_path><default_replica_name>{replica}</default_replica_name>
<max_connections>200</max_connections><max_concurrent_queries>64</max_concurrent_queries><max_server_memory_usage>%d</max_server_memory_usage><mark_cache_size>67108864</mark_cache_size><uncompressed_cache_size>16777216</uncompressed_cache_size>
<background_pool_size>4</background_pool_size><background_schedule_pool_size>4</background_schedule_pool_size><background_message_broker_schedule_pool_size>2</background_message_broker_schedule_pool_size><background_distributed_schedule_pool_size>2</background_distributed_schedule_pool_size><background_buffer_flush_schedule_pool_size>2</background_buffer_flush_schedule_pool_size><background_move_pool_size>2</background_move_pool_size><background_fetches_pool_size>2</background_fetches_pool_size><background_common_pool_size>2</background_common_pool_size>
<merge_tree><number_of_free_entries_in_pool_to_lower_max_size_of_merge>0</number_of_free_entries_in_pool_to_lower_max_size_of_merge><number_of_free_entries_in_pool_to_execute_mutation>0</number_of_free_entries_in_pool_to_execute_mutation><number_of_free_entries_in_pool_to_execute_optimize_entire_partition>0</number_of_free_entries_in_pool_to_execute_optimize_entire_partition></merge_tree>
<logger><level>error</level><console>1</console><log remove="1"/><errorlog remove="1"/></logger><query_log remove="1"/><query_thread_log remove="1"/><text_log remove="1"/><trace_log remove="1"/><processors_profile_log remove="1"/>
<storage_configuration><disks><backup><type>local</type><path>/var/lib/clickhouse/backups/</path></backup></disks></storage_configuration><backups><allowed_disk>backup</allowed_disk><remove_backup_files_after_failure>true</remove_backup_files_after_failure></backups>
</clickhouse>`, clusterAccess, d.ID, q.Value()*3/4)
}

func safeClickHouseController(pod corev1.PodTemplateSpec) bool {
	if len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 0 {
		return false
	}
	c := pod.Spec.Containers[0]
	return c.Name == "clickhouse-operator" && c.Image == clickhouseControllerImage && !c.Resources.Limits.Cpu().IsZero() && !c.Resources.Limits.Memory().IsZero() && pod.Annotations["hakopod.io/clickhouse-security"] == "strict-tls-v1"
}

func (c *Client) prepareClickHouseSecurity(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.Engine != "clickhouse" {
		return nil
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("ClickHouse namespace ownership changed")
	}
	api := c.kube.CoreV1().Secrets(ns.Name)
	secret, err := api.Get(ctx, "database-clickhouse-access", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		app, e := api.Get(ctx, "database-credentials", metav1.GetOptions{})
		if e != nil || app.Labels[databaseOwner] != d.ID || len(app.Data["password"]) < 32 {
			return fmt.Errorf("ClickHouse application credentials are unavailable")
		}
		data := map[string][]byte{}
		for _, name := range []string{"app", "hakopod-bootstrap", "hakopod-monitor", "hakopod-recovery", "default", "interserver"} {
			password := app.Data["password"]
			if name != "app" {
				random := make([]byte, 32)
				if _, err = rand.Read(random); err != nil {
					return err
				}
				password = []byte(hex.EncodeToString(random))
			}
			data[name+"-hash"] = []byte(fmt.Sprintf("%x", sha256.Sum256(password)))
			if name == "interserver" {
				data[name] = password
			}
			if strings.HasPrefix(name, "hakopod-") {
				data[strings.TrimPrefix(name, "hakopod-")+".xml"] = []byte("<config><host>127.0.0.1</host><port>9000</port><user>" + name + "</user><password>" + string(password) + "</password><send_logs_level>none</send_logs_level></config>")
			}
		}
		secret = &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-clickhouse-access"), Immutable: ptr(true), Data: data}
		if err = before(); err != nil {
			return err
		}
		secret, err = api.Create(ctx, secret, metav1.CreateOptions{})
	}
	if err != nil {
		return err
	}
	if err = databaseIdentityOwned(secret, d, ns.UID); err != nil {
		return err
	}
	if secret.Immutable == nil || !*secret.Immutable || len(secret.Data["interserver"]) != 64 {
		return fmt.Errorf("ClickHouse internal credential identity changed")
	}
	for _, name := range []string{"app", "hakopod-bootstrap", "hakopod-monitor", "hakopod-recovery", "default", "interserver"} {
		if len(secret.Data[name+"-hash"]) != 64 {
			return fmt.Errorf("ClickHouse internal credential identity changed")
		}
	}
	app, err := api.Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(secret.Data["app-hash"], []byte(fmt.Sprintf("%x", sha256.Sum256(app.Data["password"])))) {
		return fmt.Errorf("ClickHouse application credential identity changed")
	}
	return c.applyClickHouseKeeper(ctx, d, before)
}
