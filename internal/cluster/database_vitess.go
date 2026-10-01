package cluster

import (
	"crypto/md5" // The pinned operator uses this algorithm for resource names, not security.
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	vitessOperatorSource     = "10a3b742c02c38f97d554739d5a257197daa48f9"
	vitessServerSource       = "0f1ed062dec171e0adfab796110549752901e299"
	vitessOperatorImage      = "docker.io/planetscale/vitess-operator:v2.16.0@sha256:f0a6f6beec2a5e54872eadc84fca43c3093a076b74dc1a287ad78c35f1c772e2"
	vitessServerImage        = "docker.io/vitess/lite:v23.0.6@sha256:4a6fee36e049abe76e442eb43d7d198f5fe285d480e1888ee36b3a22b1914a76"
	vitessEtcdImage          = "quay.io/coreos/etcd:v3.5.17@sha256:a055da833a7c013b836ed0822e8ec1f99b059658be255ad8d0fcd31b635ae3d6"
	vitessComponentLabel     = "hakopod.io/vitess-component"
	vitessIdentityAnnotation = "hakopod.io/vitess-identity"
	vitessConfigSecret       = "database-vitess-config"
	vitessTLSPath            = "/etc/hakopod/tls"
	vitessConfigPath         = "/etc/hakopod/vitess"
)

var vitessDatabaseResource = schema.GroupVersionResource{Group: "planetscale.com", Version: "v2", Resource: "vitessclusters"}
var vitessShardResource = schema.GroupVersionResource{Group: "planetscale.com", Version: "v2", Resource: "vitessshards"}

func vitessGeneratedName(parts ...string) string {
	h := md5.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return strings.Join(parts, "-") + "-" + hex.EncodeToString(h.Sum(nil)[:4])
}

func vitessResources(cpu, memory string) map[string]any {
	return map[string]any{"requests": map[string]any{"cpu": cpu, "memory": memory}, "limits": map[string]any{"cpu": cpu, "memory": memory}}
}

func vitessClaim(size int64) map[string]any {
	return map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", size)}}}
}

func vitessSecretSource(name, key string) map[string]any {
	return map[string]any{"name": name, "key": key}
}

func vitessVolumes() []any {
	return []any{
		map[string]any{"name": "hakopod-tls", "secret": map[string]any{"secretName": "database-tls", "defaultMode": int64(0440)}},
		map[string]any{"name": "hakopod-vitess", "secret": map[string]any{"secretName": vitessConfigSecret, "defaultMode": int64(0440)}},
	}
}

func vitessMounts() []any {
	return []any{map[string]any{"name": "hakopod-tls", "mountPath": vitessTLSPath, "readOnly": true}, map[string]any{"name": "hakopod-vitess", "mountPath": vitessConfigPath, "readOnly": true}}
}

func vitessComponent(d database.Resource, name, cpu, memory string) map[string]any {
	return map[string]any{"resources": vitessResources(cpu, memory), "extraVolumes": vitessVolumes(), "extraVolumeMounts": vitessMounts(), "extraLabels": map[string]any{databaseOwner: d.ID, managedBy: "hakopod", vitessComponentLabel: name}, "affinity": vitessNodeAffinity(nil, nil)}
}

func vitessTopologyFlags() map[string]any {
	return map[string]any{"topo-etcd-tls-ca": vitessTLSPath + "/ca.crt", "topo-etcd-tls-cert": vitessTLSPath + "/tls.crt", "topo-etcd-tls-key": vitessTLSPath + "/tls.key", "logtostderr": "true"}
}

func vitessTabletClientTLSFlags(d database.Resource) map[string]any {
	return map[string]any{
		"tablet-manager-grpc-ca": vitessTLSPath + "/ca.crt", "tablet-manager-grpc-server-name": "database-internal." + DatabaseNamespace(d.ID) + ".svc.cluster.local",
	}
}

func vitessTabletTLSFlags(d database.Resource) map[string]any {
	flags := vitessTabletClientTLSFlags(d)
	flags["grpc-cert"], flags["grpc-key"] = vitessTLSPath+"/tls.crt", vitessTLSPath+"/tls.key"
	return flags
}

// vitessDatabaseSpec is a candidate desired resource. Availability is gated
// separately: a generated manifest is not evidence of native TLS or recovery.
func vitessDatabaseSpec(d database.Resource, resources map[string]any) map[string]any {
	ns := DatabaseNamespace(d.ID)
	globalEtcd := vitessGeneratedName("database", "etcd")
	etcd := vitessComponent(d, "topology", database.VitessTopologyCPU, database.VitessTopologyMemory)
	etcd["image"] = vitessEtcdImage
	etcd["dataVolumeClaimTemplate"] = vitessClaim(database.VitessTopologyStorageGiB)
	peers := make([]any, 3)
	for i := range peers {
		peers[i] = fmt.Sprintf("https://%s-%d.%s-peer.%s.svc.cluster.local:2380", globalEtcd, i+1, globalEtcd, ns)
	}
	etcd["advertisePeerURLs"] = peers
	etcd["extraFlags"] = map[string]any{
		"listen-client-urls": "https://0.0.0.0:2379", "listen-peer-urls": "https://0.0.0.0:2380",
		"advertise-client-urls": "https://$(POD_NAME)." + globalEtcd + "-peer." + ns + ".svc.cluster.local:2379",
		"cert-file":             vitessTLSPath + "/tls.crt", "key-file": vitessTLSPath + "/tls.key", "trusted-ca-file": vitessTLSPath + "/ca.crt", "client-cert-auth": "true",
		"peer-cert-file": vitessTLSPath + "/tls.crt", "peer-key-file": vitessTLSPath + "/tls.key", "peer-trusted-ca-file": vitessTLSPath + "/ca.crt", "peer-client-cert-auth": "true",
		"tls-min-version": "TLS1.2",
	}
	etcd["extraEnv"] = []any{
		map[string]any{"name": "POD_NAME", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.name"}}},
		// The peer Service publishes pod addresses before readiness. A probe
		// through the ready-only client Service would prevent first startup.
		map[string]any{"name": "ETCDCTL_ENDPOINTS", "value": "https://$(POD_NAME)." + globalEtcd + "-peer." + ns + ".svc.cluster.local:2379"},
		map[string]any{"name": "ETCDCTL_CACERT", "value": vitessTLSPath + "/ca.crt"}, map[string]any{"name": "ETCDCTL_CERT", "value": vitessTLSPath + "/tls.crt"}, map[string]any{"name": "ETCDCTL_KEY", "value": vitessTLSPath + "/tls.key"},
		// Override the operator's environment defaults, without also setting
		// CLI flags that etcd rejects when the same environment option exists.
		map[string]any{"name": "ETCD_QUOTA_BACKEND_BYTES", "value": "536870912"}, map[string]any{"name": "ETCD_MAX_REQUEST_BYTES", "value": "1048576"},
	}
	gate := vitessComponent(d, "gateway", database.VitessGatewayCPU, database.VitessGatewayMemory)
	gate["replicas"] = int64(d.Spec.VitessGateways())
	gate["authentication"] = map[string]any{"static": map[string]any{"secret": vitessSecretSource(vitessConfigSecret, "users.json")}}
	gate["secureTransport"] = map[string]any{"required": true, "tls": map[string]any{"certSecret": vitessSecretSource("database-tls", "tls.crt"), "keySecret": vitessSecretSource("database-tls", "tls.key")}}
	gate["extraFlags"] = map[string]any{"tablet-grpc-server-name": "database-internal." + ns + ".svc.cluster.local", "tablet-grpc-ca": vitessTLSPath + "/ca.crt", "mysql-server-read-timeout": "60s", "mysql-server-write-timeout": "30s", "mysql-server-query-timeout": "30s", "mysql-server-tls-min-version": "TLSv1.2", "query-timeout": "30000", "transaction-mode": "SINGLE", "ddl-strategy": "direct"}
	control := vitessComponent(d, "control", database.VitessControlCPU, database.VitessControlMemory)
	control["cells"], control["replicas"] = []any{"local"}, int64(1)
	control["extraFlags"] = vitessTabletTLSFlags(d)
	orchestrator := vitessComponent(d, "orchestrator", database.VitessControlCPU, database.VitessControlMemory)
	orchestrator["extraFlags"] = vitessTabletClientTLSFlags(d)
	tablet := vitessComponent(d, "tablet", database.VitessTabletCPU, database.VitessTabletMemory)
	delete(tablet, "resources")
	// The patched operator mounts a Pod-local 16Mi tmpfs at /vt/socket
	// without subPath. gVisor must share that mount between both processes.
	tablet["annotations"] = map[string]any{"dev.gvisor.spec.mount.rundir.share": "pod", "dev.gvisor.spec.mount.rundir.type": "tmpfs", "dev.gvisor.spec.mount.rundir.options": "rw,rprivate,size=16777216"}
	tablet["cell"], tablet["type"], tablet["replicas"] = "local", "replica", int64(1+d.Spec.Replicas)
	tablet["dataVolumeClaimTemplate"] = vitessClaim(d.Spec.StorageGiB)
	tablet["extraEnv"] = []any{map[string]any{"name": "POD_NAME", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.name"}}}}
	flags := vitessTabletTLSFlags(d)
	flags["tablet-hostname"] = "$(POD_NAME)." + ns + ".svc.cluster.local"
	flags["db-credentials-file"] = vitessConfigPath + "/db-credentials.json"
	flags["table-acl-config"], flags["enforce-tableacl-config"], flags["queryserver-config-strict-table-acl"] = vitessConfigPath+"/table-acl.json", "true", "true"
	flags["db-ssl-mode"], flags["db-ssl-ca"] = "verify_identity", vitessTLSPath+"/ca.crt"
	// Local MySQL users connect through the shared Unix socket. Only the
	// replication user connects over TCP and must verify its primary's TLS.
	for _, account := range []string{"app", "appdebug", "allprivs", "dba", "filtered"} {
		flags["db-"+account+"-use-ssl"] = "false"
	}
	flags["db-repl-use-ssl"] = "true"
	flags["queryserver-config-query-timeout"], flags["queryserver-config-pool-size"], flags["queryserver-config-stream-pool-size"], flags["queryserver-config-transaction-cap"] = "30s", "32", "8", "32"
	tablet["vttablet"] = map[string]any{"resources": vitessResources(database.VitessTabletCPU, database.VitessTabletMemory), "extraFlags": flags, "terminationGracePeriodSeconds": int64(120)}
	tablet["mysqld"] = map[string]any{"resources": resources, "configOverrides": "[mysqld]\nrequire_secure_transport=ON\ntls_version=TLSv1.2,TLSv1.3\nssl_ca=" + vitessTLSPath + "/ca.crt\nssl_cert=" + vitessTLSPath + "/tls.crt\nssl_key=" + vitessTLSPath + "/tls.key\ngeneral_log=OFF\nslow_query_log=OFF\nlocal_infile=OFF\nmax_connections=200\ninnodb_buffer_pool_size=268435456\n"}
	durability := "none"
	if d.Spec.Mode == "cluster" {
		durability = "semi_sync"
	}
	return map[string]any{
		"images":           map[string]any{"vtctld": vitessServerImage, "vtgate": vitessServerImage, "vttablet": vitessServerImage, "vtorc": vitessServerImage, "vtbackup": vitessServerImage, "mysqld": map[string]any{"mysql80Compatible": vitessServerImage}},
		"globalLockserver": map[string]any{"etcd": etcd}, "extraVitessFlags": vitessTopologyFlags(),
		"cells": []any{map[string]any{"name": "local", "gateway": gate}}, "vitessDashboard": control,
		"keyspaces":      []any{map[string]any{"name": "app", "databaseName": "app", "durabilityPolicy": durability, "turndownPolicy": "RequireIdle", "vitessOrchestrator": orchestrator, "partitionings": []any{map[string]any{"equal": map[string]any{"parts": int64(d.Spec.Shards), "shardTemplate": map[string]any{"databaseInitScriptSecret": vitessSecretSource(vitessConfigSecret, "init.sql"), "tabletPools": []any{tablet}}}}}}},
		"updateStrategy": map[string]any{"type": "Immediate"},
	}
}

// A release must replace this gate only after all named-development-cluster
// acceptance requirements in managed-vitess.md have passed for pinned images.
func vitessRuntimeSupported(s database.Spec) error {
	if err := s.ValidateVitess(); err != nil {
		return err
	}
	return fmt.Errorf("managed Vitess is unavailable: native replication verification and recovery acceptance are incomplete")
}

func vitessIdentityNames(d database.Resource) []string {
	ns := DatabaseNamespace(d.ID)
	etcd := vitessGeneratedName("database", "etcd")
	names := []string{"database", "database." + ns + ".svc", "database." + ns + ".svc.cluster.local", "database-internal." + ns + ".svc.cluster.local", "*." + ns + ".svc.cluster.local", etcd + "-client." + ns + ".svc", etcd + "-client." + ns + ".svc.cluster.local"}
	for i := 1; i <= 3; i++ {
		names = append(names, fmt.Sprintf("%s-%d.%s-peer.%s.svc.cluster.local", etcd, i, etcd, ns))
	}
	return names
}

func applyVitessPolicy(object *unstructured.Unstructured, s database.Spec, p DatabasePolicy) {
	nodes := p.nodes()
	if len(s.Placement.NodeNames) != 0 {
		nodes = s.Placement.NodeNames
	}
	mutateVitessComponents(object, func(item map[string]any, role string) {
		item["affinity"] = vitessNodeAffinity(nodes, &p)
		item["tolerations"] = []any{map[string]any{"key": "hakopod.com/pool", "operator": "Equal", "value": p.Pool, "effect": "NoSchedule"}}
		if claim, ok := item["dataVolumeClaimTemplate"].(map[string]any); ok {
			claim["storageClassName"] = p.StorageClass
		}
	})
	applyVitessPlacement(object, s, nodes)
}

func vitessNodeAffinity(nodes []string, p *DatabasePolicy) map[string]any {
	expressions := []any{map[string]any{"key": "kubernetes.io/arch", "operator": "In", "values": []any{"amd64"}}}
	if p != nil {
		expressions = append(expressions, map[string]any{"key": "hakopod.com/pool", "operator": "In", "values": []any{p.Pool}}, map[string]any{"key": DatabaseDefaultRuntimeLabel, "operator": "In", "values": []any{p.RuntimeClass}})
	}
	term := map[string]any{"matchExpressions": expressions}
	terms := []any{term}
	if len(nodes) > 0 {
		terms = make([]any, len(nodes))
		for i, node := range nodes {
			terms[i] = map[string]any{"matchExpressions": expressions, "matchFields": []any{map[string]any{"key": "metadata.name", "operator": "In", "values": []any{node}}}}
		}
	}
	return map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{"nodeSelectorTerms": terms}}}
}

func mutateVitessComponents(object *unstructured.Unstructured, component func(map[string]any, string)) {
	spec := object.Object["spec"].(map[string]any)
	component(spec["globalLockserver"].(map[string]any)["etcd"].(map[string]any), "topology")
	component(spec["vitessDashboard"].(map[string]any), "control")
	for _, cell := range spec["cells"].([]any) {
		component(cell.(map[string]any)["gateway"].(map[string]any), "gateway")
	}
	for _, raw := range spec["keyspaces"].([]any) {
		keyspace := raw.(map[string]any)
		component(keyspace["vitessOrchestrator"].(map[string]any), "orchestrator")
		for _, raw := range keyspace["partitionings"].([]any) {
			for _, pool := range raw.(map[string]any)["equal"].(map[string]any)["shardTemplate"].(map[string]any)["tabletPools"].([]any) {
				component(pool.(map[string]any), "tablet")
			}
		}
	}
}

func applyVitessPlacement(object *unstructured.Unstructured, s database.Spec, nodes []string) {
	mutateVitessComponents(object, func(item map[string]any, role string) {
		affinity, ok := item["affinity"].(map[string]any)
		if !ok {
			affinity = vitessNodeAffinity(nodes, nil)
		}
		if len(nodes) > 0 {
			nodeAffinity := affinity["nodeAffinity"].(map[string]any)
			required := nodeAffinity["requiredDuringSchedulingIgnoredDuringExecution"].(map[string]any)
			terms := required["nodeSelectorTerms"].([]any)
			expressions := terms[0].(map[string]any)["matchExpressions"]
			terms = make([]any, len(nodes))
			for i, node := range nodes {
				terms[i] = map[string]any{"matchExpressions": expressions, "matchFields": []any{map[string]any{"key": "metadata.name", "operator": "In", "values": []any{node}}}}
			}
			required["nodeSelectorTerms"] = terms
		}
		if s.Placement.Spread != "" && (role == "tablet" || role == "gateway" || role == "topology") {
			affinity["podAntiAffinity"] = map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{"topologyKey": databaseTopologyKey(s), "labelSelector": map[string]any{"matchLabels": map[string]any{vitessComponentLabel: role}}}}}
		}
		item["affinity"] = affinity
	})
}
