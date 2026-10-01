package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func vitessAccountData(s database.Spec, appPassword, replicationPassword []byte) (map[string][]byte, error) {
	if len(appPassword) < 32 || len(appPassword) > 128 || bytes.ContainsAny(appPassword, "\r\n\x00") || len(replicationPassword) != 32 {
		return nil, fmt.Errorf("Vitess credential format is invalid")
	}
	if _, err := hex.DecodeString(string(replicationPassword)); err != nil {
		return nil, fmt.Errorf("Vitess replication credential is invalid")
	}
	vschema, err := s.VitessVSchema()
	if err != nil {
		return nil, err
	}
	users, err := json.Marshal(map[string]any{"app": []any{map[string]any{"UserData": "app", "Password": string(appPassword)}}})
	if err != nil {
		return nil, err
	}
	credentials, err := json.Marshal(map[string][]string{"vt_repl": {string(replicationPassword)}})
	if err != nil {
		return nil, err
	}
	acl, err := json.Marshal(map[string]any{"table_groups": []any{map[string]any{"name": "application", "table_names_or_prefixes": []string{"%"}, "readers": []string{"app"}, "writers": []string{"app"}, "admins": []string{"app"}}}})
	if err != nil {
		return nil, err
	}
	// mysqlctld and vttablet use this administrative account over a shared
	// Unix socket. No remote administrative account or password is created.
	initSQL := "SET @prior_super_read_only=@@global.super_read_only; SET GLOBAL super_read_only=OFF; SET sql_log_bin=0;\n" +
		"DELETE FROM mysql.user WHERE User='' OR (User='root' AND Host!='localhost'); DROP DATABASE IF EXISTS test;\n" +
		"CREATE DATABASE IF NOT EXISTS _vt; CREATE DATABASE IF NOT EXISTS app;\n" +
		"CREATE USER 'vt_dba'@'localhost'; GRANT ALL ON *.* TO 'vt_dba'@'localhost' WITH GRANT OPTION;\n" +
		"CREATE USER 'vt_app'@'localhost'; GRANT ALL ON app.* TO 'vt_app'@'localhost';\n" +
		"CREATE USER 'vt_appdebug'@'localhost'; GRANT SELECT ON app.* TO 'vt_appdebug'@'localhost';\n" +
		"CREATE USER 'vt_allprivs'@'localhost'; GRANT ALL ON app.* TO 'vt_allprivs'@'localhost'; GRANT ALL ON _vt.* TO 'vt_allprivs'@'localhost';\n" +
		"CREATE USER 'vt_filtered'@'localhost'; GRANT ALL ON app.* TO 'vt_filtered'@'localhost'; GRANT ALL ON _vt.* TO 'vt_filtered'@'localhost';\n" +
		"CREATE USER 'vt_repl'@'%' IDENTIFIED BY '" + string(replicationPassword) + "' REQUIRE SSL; GRANT REPLICATION SLAVE ON *.* TO 'vt_repl'@'%';\n" +
		"SET GLOBAL super_read_only=@prior_super_read_only;\n"
	return map[string][]byte{"users.json": users, "db-credentials.json": credentials, "replication-password": replicationPassword, "init.sql": []byte(initSQL), "vschema.json": vschema, "table-acl.json": acl}, nil
}

func (c *Client) prepareVitessSecurity(ctx context.Context, d database.Resource, password []byte, before func() error) error {
	if d.Spec.Engine != "vitess" {
		return nil
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess namespace ownership changed")
	}
	api := c.kube.CoreV1().Secrets(ns.Name)
	secret, err := api.Get(ctx, vitessConfigSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// MySQL replication passwords are limited to 32 characters. Hex
		// encodes 16 random bytes into that limit while retaining 128 bits.
		passwordBytes := make([]byte, 16)
		if _, err = rand.Read(passwordBytes); err != nil {
			return err
		}
		data, err := vitessAccountData(d.Spec, password, []byte(hex.EncodeToString(passwordBytes)))
		if err != nil {
			return err
		}
		secret = &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, vitessConfigSecret), Immutable: ptr(true), Data: data}
		if err = before(); err != nil {
			return err
		}
		if _, err = api.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return err
		}
	} else {
		if err != nil {
			return err
		}
		if err = databaseIdentityOwned(secret, d, ns.UID); err != nil {
			return err
		}
		want, err := vitessAccountData(d.Spec, password, secret.Data["replication-password"])
		if err != nil || secret.Immutable == nil || !*secret.Immutable || !reflect.DeepEqual(want, secret.Data) {
			return fmt.Errorf("Vitess immutable credentials or routing schema changed")
		}
	}
	return nil
}

func vitessOperatorObject(d database.Resource, namespaceUID types.UID) *unstructured.Unstructured {
	ns := DatabaseNamespace(d.ID)
	labels := map[string]any{managedBy: "hakopod", databaseOwner: d.ID, vitessComponentLabel: "operator"}
	field := func(name, value string) any { return map[string]any{"name": name, "value": value} }
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "vitess-operator", "namespace": ns, "labels": labels, "ownerReferences": []any{map[string]any{"apiVersion": "v1", "kind": "Namespace", "name": ns, "uid": string(namespaceUID)}}},
		"spec": map[string]any{"replicas": int64(1), "selector": map[string]any{"matchLabels": labels}, "strategy": map[string]any{"type": "Recreate"}, "template": map[string]any{
			"metadata": map[string]any{"labels": labels}, "spec": map[string]any{"serviceAccountName": "database-vitess-operator", "nodeSelector": map[string]any{"kubernetes.io/arch": "amd64"}, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": int64(1000), "runAsGroup": int64(1000), "fsGroup": int64(1000)}, "volumes": []any{map[string]any{"name": "hakopod-tls", "secret": map[string]any{"secretName": "database-tls", "defaultMode": int64(0440)}}}, "containers": []any{map[string]any{
				"name": "vitess-operator", "image": vitessOperatorImage, "command": []any{"vitess-operator"}, "args": []any{"--logtostderr", "--v=0", "--zap-log-level=error", "--reconcile_timeout=2m", "--default_vitess_priority_class=", "--default_vitess_service_account=database-vitess-workload", "--default_etcd_service_account=database-vitess-workload", "--default_mysqld_exporter_image=", "--topo-etcd-tls-ca=" + vitessTLSPath + "/ca.crt", "--topo-etcd-tls-cert=" + vitessTLSPath + "/tls.crt", "--topo-etcd-tls-key=" + vitessTLSPath + "/tls.key", "--tablet-manager-grpc-ca=" + vitessTLSPath + "/ca.crt", "--tablet-manager-grpc-server-name=database-internal." + ns + ".svc.cluster.local"},
				"env":       []any{field("WATCH_NAMESPACE", ns), field("POD_NAMESPACE", ns), field("PS_OPERATOR_POD_NAMESPACE", ns), field("OPERATOR_NAME", "vitess-operator"), map[string]any{"name": "POD_NAME", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.name"}}}, map[string]any{"name": "PS_OPERATOR_POD_NAME", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.name"}}}},
				"resources": vitessResources(database.VitessOperatorCPU, database.VitessOperatorMemory), "securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []any{"ALL"}}}, "volumeMounts": []any{map[string]any{"name": "hakopod-tls", "mountPath": vitessTLSPath, "readOnly": true}},
			}}},
		}},
	}}
}

func vitessRoleRules() []any {
	rule := func(group string, resources []any) any {
		return map[string]any{"apiGroups": []any{group}, "resources": resources, "verbs": []any{"get", "list", "watch", "create", "update", "patch", "delete"}}
	}
	return []any{
		rule("", []any{"pods", "services", "endpoints", "persistentvolumeclaims", "events", "configmaps", "secrets"}),
		rule("apps", []any{"deployments", "deployments/finalizers", "replicasets", "statefulsets"}),
		rule("batch", []any{"jobs"}), rule("policy", []any{"poddisruptionbudgets"}), rule("coordination.k8s.io", []any{"leases"}), rule("autoscaling", []any{"horizontalpodautoscalers"}),
		rule("planetscale.com", []any{"vitessclusters", "vitessclusters/status", "vitessclusters/finalizers", "vitesscells", "vitesscells/status", "vitesscells/finalizers", "vitesskeyspaces", "vitesskeyspaces/status", "vitesskeyspaces/finalizers", "vitessshards", "vitessshards/status", "vitessshards/finalizers", "etcdlockservers", "etcdlockservers/status", "etcdlockservers/finalizers", "vitessbackups", "vitessbackups/status", "vitessbackups/finalizers", "vitessbackupstorages", "vitessbackupstorages/status", "vitessbackupstorages/finalizers", "vitessbackupschedules", "vitessbackupschedules/status", "vitessbackupschedules/finalizers"}),
	}
}

func (c *Client) applyVitessOwnedObject(ctx context.Context, d database.Resource, nsUID types.UID, gvr schema.GroupVersionResource, object *unstructured.Unstructured, before func() error) error {
	api := c.dynamic.Resource(gvr).Namespace(DatabaseNamespace(d.ID))
	old, err := api.Get(ctx, object.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = api.Create(ctx, object, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	owned := false
	for _, ref := range old.GetOwnerReferences() {
		if ref.UID == nsUID && ref.Kind == "Namespace" && ref.Name == DatabaseNamespace(d.ID) {
			owned = true
		}
	}
	if !owned || old.GetLabels()[databaseOwner] != d.ID || old.GetLabels()[managedBy] != "hakopod" || old.GetDeletionTimestamp() != nil {
		return fmt.Errorf("Vitess supporting resource ownership changed")
	}
	updated := old.DeepCopy()
	for _, field := range []string{"spec", "rules", "subjects", "roleRef", "automountServiceAccountToken"} {
		if value, ok := object.Object[field]; ok {
			updated.Object[field] = value
		}
	}
	if reflect.DeepEqual(old.Object, updated.Object) {
		return nil
	}
	if err = before(); err != nil {
		return err
	}
	_, err = api.Update(ctx, updated, metav1.UpdateOptions{})
	return err
}

// A namespace-scoped operator keeps its process-wide topology trust and
// Kubernetes writes inside one managed database's security boundary.
func (c *Client) prepareVitessController(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.Engine != "vitess" {
		return nil
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess operator namespace ownership changed")
	}
	metadata := func(name string) map[string]any {
		return map[string]any{"name": name, "namespace": ns.Name, "labels": map[string]any{databaseOwner: d.ID, managedBy: "hakopod"}, "ownerReferences": []any{map[string]any{"apiVersion": "v1", "kind": "Namespace", "name": ns.Name, "uid": string(ns.UID)}}}
	}
	items := []struct {
		gvr    schema.GroupVersionResource
		object map[string]any
	}{
		{schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}, map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": metadata("database-vitess-workload"), "automountServiceAccountToken": false}},
		{schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}, map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": metadata("database-vitess-operator"), "automountServiceAccountToken": true}},
		{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}, map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": metadata("database-vitess-operator"), "rules": vitessRoleRules()}},
		{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}, map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": metadata("database-vitess-operator"), "roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "database-vitess-operator"}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": "database-vitess-operator", "namespace": ns.Name}}}},
	}
	for _, item := range items {
		if err = c.applyVitessOwnedObject(ctx, d, ns.UID, item.gvr, &unstructured.Unstructured{Object: item.object}, before); err != nil {
			return err
		}
	}
	object := vitessOperatorObject(d, ns.UID)
	identity, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = databaseIdentityOwned(identity, d, ns.UID); err != nil {
		return err
	}
	if err = unstructured.SetNestedField(object.Object, fmt.Sprintf("%x", sha256.Sum256(identity.Data["tls.crt"])), "spec", "template", "metadata", "annotations", "hakopod.io/vitess-identity"); err != nil {
		return err
	}
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return err
	}
	nodes := d.Spec.Placement.NodeNames
	if policy != nil && len(nodes) == 0 {
		nodes = policy.nodes()
	}
	if err = unstructured.SetNestedMap(object.Object, vitessNodeAffinity(nodes, policy), "spec", "template", "spec", "affinity"); err != nil {
		return err
	}
	if policy != nil {
		if err = unstructured.SetNestedSlice(object.Object, []any{map[string]any{"key": "hakopod.com/pool", "operator": "Equal", "value": policy.Pool, "effect": "NoSchedule"}}, "spec", "template", "spec", "tolerations"); err != nil {
			return err
		}
	}
	return c.applyVitessOwnedObject(ctx, d, ns.UID, schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, object, before)
}
