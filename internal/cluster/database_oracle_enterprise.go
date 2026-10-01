package cluster

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const oracleEnterpriseControllerSource = "ff6f9178c1650df30afbf203ebdb633e9b80760a"
const oracleEnterprisePolicy = "hakopod-oracle-tcps-v1"
const oracleEnterprisePodPolicy = "hakopod.io/oracle-pod-policy"
const oracleEnterpriseMemberLabel = "hakopod.io/oracle-member"

// A source adapter is not native qualification. This remains false until the
// patched operator, licensed image, recovery and partition acceptance pass.
// There is deliberately no user setting or environment variable to bypass it.
const oracleEnterpriseAccepted = false

var oracleEnterpriseResource = schema.GroupVersionResource{Group: "database.oracle.com", Version: "v4", Resource: "singleinstancedatabases"}
var oracleEnterpriseBrokerResource = schema.GroupVersionResource{Group: "database.oracle.com", Version: "v4", Resource: "dataguardbrokers"}

func oracleEnterprise(s database.Spec) bool {
	return s.Engine == "oracle" && s.Oracle != nil && s.Oracle.Edition == "enterprise"
}

func oracleEnterpriseMemberName(index int) string {
	if index == 0 {
		return "database"
	}
	return "database-" + strconv.Itoa(index)
}

func oracleEnterpriseSID(index int) string { return "HPDB" + strconv.Itoa(index) }

func oraclePDB(d database.Resource) string {
	if oracleEnterprise(d.Spec) {
		return "APPDB"
	}
	return "FREEPDB1"
}

func oracleEnterpriseNames(d database.Resource) []string {
	names := []string{"database-rw." + DatabaseNamespace(d.ID) + ".svc"}
	for i := 0; i < d.Spec.Members(); i++ {
		name := oracleEnterpriseMemberName(i)
		names = append(names, name, name+"."+DatabaseNamespace(d.ID), name+"."+DatabaseNamespace(d.ID)+".svc", name+"."+DatabaseNamespace(d.ID)+".svc.cluster.local")
	}
	return names
}

func (c *Client) oracleEnterpriseNamespace(ctx context.Context, d database.Resource) (*corev1.Namespace, error) {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.DeletionTimestamp != nil || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" || ns.Labels["hakopod.io/project"] != d.Project || ns.Labels["hakopod.io/environment"] != d.Environment {
		return nil, fmt.Errorf("Oracle Enterprise namespace identity or scope changed")
	}
	return ns, nil
}

func safeOracleEnterpriseController(pod corev1.PodTemplateSpec) bool {
	if pod.Annotations["hakopod.io/oracle-controller-source"] != oracleEnterpriseControllerSource || pod.Annotations["hakopod.io/oracle-security-policy"] != oracleEnterprisePolicy || len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 0 {
		return false
	}
	container := pod.Spec.Containers[0]
	_, digest, pinned := strings.Cut(container.Image, "@sha256:")
	decoded, err := hex.DecodeString(digest)
	if !pinned || err != nil || len(decoded) != 32 || container.Name != "manager" || container.Resources.Requests.Cpu().IsZero() || container.Resources.Requests.Memory().IsZero() || container.Resources.Limits.Cpu().IsZero() || container.Resources.Limits.Memory().IsZero() {
		return false
	}
	for _, env := range container.Env {
		if env.Name == "HAKOPOD_ORACLE_POLICY" && env.ValueFrom == nil && env.Value == oracleEnterprisePolicy {
			return true
		}
	}
	return false
}

func (c *Client) oracleEnterpriseControllerAvailable(ctx context.Context) error {
	if !oracleEnterpriseAccepted {
		return fmt.Errorf("Oracle Enterprise and Data Guard are awaiting licensed native acceptance of the hardened operator")
	}
	return c.oracleEnterpriseControllerReady(ctx)
}

// The separate check also serves explicit native qualification tests. Calling
// it does not enable the public create or reconciliation path.
func (c *Client) oracleEnterpriseControllerReady(ctx context.Context) error {
	deployment, err := c.kube.AppsV1().Deployments("oracle-database-operator-system").Get(ctx, "oracle-database-operator-controller-manager", metav1.GetOptions{})
	if err != nil || deployment.DeletionTimestamp != nil || deployment.Status.ObservedGeneration < deployment.Generation || deployment.Status.UpdatedReplicas != 1 || deployment.Status.AvailableReplicas != 1 || deployment.Status.Replicas != 1 || !safeOracleEnterpriseController(deployment.Spec.Template) {
		return fmt.Errorf("Oracle Enterprise requires the pinned, hardened operator with a current available deployment")
	}
	for _, gvr := range []schema.GroupVersionResource{oracleEnterpriseResource, oracleEnterpriseBrokerResource} {
		if _, err = c.dynamic.Resource(gvr).Namespace("oracle-database-operator-system").List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
			return fmt.Errorf("Oracle Enterprise controller APIs are unavailable")
		}
	}
	return nil
}

// These annotations are consumed only by the pinned upstream patch. They carry
// the same allocation rules as other engines without granting a general pod
// template to database users.
func oracleEnterprisePodEnvelope(d database.Resource, p *DatabasePolicy, nodes []string, helper bool) string {
	labels := databaseLabels(d)
	if d.Spec.Oracle.RegistryCredential != "" {
		labels[registryScopeLabel] = RegistryScope(d.Project, d.Environment, d.Spec.Oracle.RegistryCredential)
	}
	resources := map[string]any{"requests": map[string]any{"cpu": d.Spec.CPU, "memory": d.Spec.Memory, "ephemeral-storage": "256Mi"}, "limits": map[string]any{"cpu": d.Spec.CPU, "memory": d.Spec.Memory, "ephemeral-storage": "2Gi"}}
	if helper {
		resources = map[string]any{"requests": map[string]any{"cpu": database.OracleBrokerCPU, "memory": database.OracleBrokerMemory, "ephemeral-storage": "128Mi"}, "limits": map[string]any{"cpu": database.OracleBrokerCPU, "memory": database.OracleBrokerMemory, "ephemeral-storage": "1Gi"}}
	} else {
		labels[oracleEnterpriseMemberLabel] = "true"
	}
	policy := map[string]any{"profile": oracleEnterprisePolicy, "labels": labels, "resources": resources}
	if len(nodes) > 0 {
		terms := make([]any, 0, len(nodes))
		for _, name := range nodes {
			terms = append(terms, map[string]any{"matchFields": []any{map[string]any{"key": "metadata.name", "operator": "In", "values": []any{name}}}})
		}
		policy["affinity"] = map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{"nodeSelectorTerms": terms}}}
	}
	if !helper && d.Spec.Placement.Spread != "" {
		affinity, _ := policy["affinity"].(map[string]any)
		if affinity == nil {
			affinity = map[string]any{}
		}
		affinity["podAntiAffinity"] = map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{"topologyKey": databaseTopologyKey(d.Spec), "labelSelector": map[string]any{"matchLabels": map[string]any{databaseOwner: d.ID, oracleEnterpriseMemberLabel: "true"}}}}}
		policy["affinity"] = affinity
	}
	if p != nil {
		policy["nodeSelector"] = map[string]string{"hakopod.com/pool": p.Pool, DatabaseDefaultRuntimeLabel: p.RuntimeClass}
		policy["runtimeClassName"] = p.RuntimeClass
		policy["tolerations"] = []any{map[string]any{"key": "hakopod.com/pool", "operator": "Equal", "value": p.Pool, "effect": "NoSchedule"}}
	}
	encoded, _ := json.Marshal(policy)
	return string(encoded)
}

func oracleEnterpriseObject(d database.Resource, index int, registry string, p *DatabasePolicy, nodes []string) *unstructured.Unstructured {
	name := oracleEnterpriseMemberName(index)
	resources := map[string]any{"requests": map[string]any{"cpu": d.Spec.CPU, "memory": d.Spec.Memory, "ephemeral-storage": "256Mi"}, "limits": map[string]any{"cpu": d.Spec.CPU, "memory": d.Spec.Memory, "ephemeral-storage": "2Gi"}}
	storage := func() map[string]any {
		value := map[string]any{"size": fmt.Sprintf("%dGi", d.Spec.StorageGiB), "accessMode": "ReadWriteOnce"}
		if p != nil {
			value["storageClass"] = p.StorageClass
		}
		return value
	}
	data, fra := storage(), storage()
	fra["mountPath"] = "/opt/oracle/hakopod-fra"
	// Leave headroom for filesystem metadata. The FRA is bounded on its own PVC.
	fra["recoveryAreaSize"] = fmt.Sprintf("%dG", max(1, d.Spec.StorageGiB-1))
	backup := map[string]any{"mountPath": "/opt/oracle/hakopod-backup", "pvcName": name + "-backup", "storageSizeInGb": d.Spec.StorageGiB}
	if p != nil {
		backup["storageClass"] = p.StorageClass
	}
	spec := map[string]any{
		"edition": "enterprise", "sid": oracleEnterpriseSID(index), "pdbName": oraclePDB(d), "charset": "AL32UTF8", "createAs": "primary", "replicas": int64(1),
		"image":     map[string]any{"pullFrom": d.Spec.Oracle.Image, "pullSecrets": registry, "imagePullPolicy": "IfNotPresent"},
		"resources": resources, "shmSize": "1Gi", "disableDefaultDiagVolumeClaim": true, "automountServiceAccountToken": false,
		"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": int64(54321), "runAsGroup": int64(54321), "fsGroup": int64(54321), "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
		"capabilities":    map[string]any{"drop": []any{"ALL"}},
		"security":        map[string]any{"secrets": map[string]any{"admin": map[string]any{"secretName": "database-oracle-access", "secretKey": "admin", "keepSecret": true}}, "tcps": map[string]any{"enabled": true, "tlsSecret": "database-tls"}},
		"services":        map[string]any{"endpoints": []any{map[string]any{"name": "cluster", "type": "ClusterIP", "tcp": map[string]any{"enabled": false}, "tcps": map[string]any{"enabled": true, "port": int64(2484)}}}},
		"persistence":     map[string]any{"oradata": data, "fra": fra, "additionalPVCs": []any{backup}, "setWritePermissions": false},
		"archiveLog":      true, "forceLog": true, "flashBack": true,
		"dataguard": map[string]any{"mode": "Disabled", "prereqs": map[string]any{"enabled": d.Spec.Mode == "cluster", "brokerConfigDir": "/opt/oracle/oradata/hakopod-broker"}},
	}
	if index > 0 {
		spec["createAs"] = "standby"
		spec["primarySource"] = map[string]any{"databaseRef": "database"}
	}
	labels := map[string]any{}
	for k, v := range databaseLabels(d) {
		labels[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "database.oracle.com/v4", "kind": "SingleInstanceDatabase", "metadata": map[string]any{"name": name, "namespace": DatabaseNamespace(d.ID), "labels": labels, "annotations": map[string]any{"hakopod.io/database-revision": strconv.FormatInt(d.Revision, 10), oracleEnterprisePodPolicy: oracleEnterprisePodEnvelope(d, p, nodes, false)}}, "spec": spec}}
}

func oracleEnterpriseBroker(d database.Resource, registry string, p *DatabasePolicy, nodes []string) *unstructured.Unstructured {
	members := []any{}
	for i := 0; i < d.Spec.Members(); i++ {
		name := oracleEnterpriseMemberName(i)
		role := "PHYSICAL_STANDBY"
		if i == 0 {
			role = "PRIMARY"
		}
		members = append(members, map[string]any{"name": name, "role": role, "dbUniqueName": oracleEnterpriseSID(i), "localRef": map[string]any{"apiVersion": "database.oracle.com/v4", "kind": "SingleInstanceDatabase", "name": name, "namespace": DatabaseNamespace(d.ID)}, "adminSecretRef": map[string]any{"secretName": "database-oracle-access", "secretKey": "admin"}, "tcps": map[string]any{"enabled": true, "serverTLSSecret": "database-tls", "clientWalletSecret": name + "-dg-client-wallet"}, "endpoints": []any{map[string]any{"name": "tcps", "protocol": "TCPS", "host": name + "." + DatabaseNamespace(d.ID) + ".svc", "port": int64(2484), "serviceName": oracleEnterpriseSID(i)}}})
	}
	labels := map[string]any{}
	for k, v := range databaseLabels(d) {
		labels[k] = v
	}
	execution := map[string]any{"image": d.Spec.Oracle.Image, "authWallet": map[string]any{"enabled": true, "passwordSecretRef": map[string]any{"secretName": "database-oracle-access", "secretKey": "wallet"}}, "walletMountPath": "/opt/oracle/hakopod-wallets", "tnsAdminPath": "/opt/oracle/hakopod-net"}
	if registry != "" {
		execution["imagePullSecrets"] = []any{registry}
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "database.oracle.com/v4", "kind": "DataguardBroker", "metadata": map[string]any{"name": "database-broker", "namespace": DatabaseNamespace(d.ID), "labels": labels, "annotations": map[string]any{"hakopod.io/database-revision": strconv.FormatInt(d.Revision, 10), oracleEnterprisePodPolicy: oracleEnterprisePodEnvelope(d, p, nodes, true)}}, "spec": map[string]any{"loadBalancer": false, "fastStartFailover": false, "protectionMode": "MaxAvailability", "execution": execution, "topology": map[string]any{"sourceKind": "SingleInstanceDatabase", "policy": map[string]any{"protectionMode": "MaxAvailability", "transportMode": "SYNC", "fastStartFailover": false}, "members": members, "observer": map[string]any{"enabled": false}}}}}
}
