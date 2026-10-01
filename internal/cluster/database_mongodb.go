package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var mongodbDatabaseResource = schema.GroupVersionResource{Group: "mongodbcommunity.mongodb.com", Version: "v1", Resource: "mongodbcommunity"}

const mongodbControllerImage = "ghcr.io/hakopod/managed-mongodb-operator:1.13.0-hakopod.1@sha256:7ba576ba3115f007fcfc22c38b530ad05c211705a6a958124ec41ea80239a85c"
const mongodbServerImage = "quay.io/mongodb/mongodb-community-server:8.0.32-ubi8@sha256:7c905b7efb6d7713ded906b88483d68776fbd37722a47fdaf7444b9ccdb0a86d"
const mongodbAgentImage = "quay.io/mongodb/mongodb-agent:109.0.0.9285-1@sha256:2d819aff81c5d9be80017791ad9e6f8c0fef828b476c9ed1cba8fe9838b07cd2"
const mongodbReadinessImage = "quay.io/mongodb/mongodb-kubernetes-readinessprobe:1.0.24@sha256:dd861b4254ebf0458c460e9445fcda89a5b101ad39f9719fd09c33a99511d928"
const mongodbUpgradeImage = "quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook:1.0.10@sha256:65e653bd319022328102bbd4aa8559810fe10282608af359e34e224e98116652"

func mongodbDatabaseSpec(d database.Resource, resources map[string]any) map[string]any {
	agent := map[string]any{"requests": map[string]any{"cpu": database.MongoDBAgentCPU, "memory": database.MongoDBAgentMemory}, "limits": map[string]any{"cpu": database.MongoDBAgentCPU, "memory": database.MongoDBAgentMemory}}
	claim := func(name string, size int64) any {
		return map[string]any{"metadata": map[string]any{"name": name}, "spec": map[string]any{"accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", size)}}}}
	}
	return map[string]any{
		"members": int64(d.Spec.Members()), "type": "ReplicaSet", "version": "8.0.32", "arbiters": int64(0),
		"featureCompatibilityVersion": "8.0",
		"security": map[string]any{
			"authentication": map[string]any{"modes": []any{"SCRAM-SHA-256"}},
			"roles": []any{map[string]any{
				"role": "hakopod-recovery", "db": "app",
				"roles":                      []any{map[string]any{"name": "readWrite", "db": "app"}},
				"privileges":                 []any{map[string]any{"resource": map[string]any{"db": "app", "collection": ""}, "actions": []any{"bypassDocumentValidation"}}},
				"authenticationRestrictions": []any{map[string]any{"clientSource": []any{"127.0.0.1/32"}, "serverAddress": []any{"127.0.0.1/32"}}},
			}},
			"tls": map[string]any{"enabled": true, "optional": false, "certificateKeySecretRef": map[string]any{"name": "database-tls"}, "caConfigMapRef": map[string]any{"name": "database-ca-public"}},
		},
		"users": []any{
			map[string]any{"name": "app", "db": "app", "passwordSecretRef": map[string]any{"name": "database-credentials"}, "scramCredentialsSecretName": "database-app", "roles": []any{map[string]any{"name": "readWrite", "db": "app"}}},
			map[string]any{"name": "hakopod-monitor", "db": "admin", "passwordSecretRef": map[string]any{"name": "database-monitor"}, "scramCredentialsSecretName": "database-monitor", "roles": []any{map[string]any{"name": "clusterMonitor", "db": "admin"}}},
			map[string]any{"name": "hakopod-recovery", "db": "app", "passwordSecretRef": map[string]any{"name": "database-recovery"}, "scramCredentialsSecretName": "database-recovery", "roles": []any{map[string]any{"name": "hakopod-recovery", "db": "app"}}},
		},
		"additionalMongodConfig": map[string]any{
			"net":                map[string]any{"maxIncomingConnections": int64(200), "tls": map[string]any{"disabledProtocols": "TLS1_0,TLS1_1"}},
			"storage":            map[string]any{"wiredTiger": map[string]any{"engineConfig": map[string]any{"cacheSizeGB": 0.25}}},
			"operationProfiling": map[string]any{"mode": "off", "slowOpThresholdMs": int64(2147483647)},
			"systemLog":          map[string]any{"quiet": true, "verbosity": int64(0)},
		},
		"agent": map[string]any{"logLevel": "WARN", "logFile": "/dev/stdout"},
		"statefulSet": map[string]any{"spec": map[string]any{
			"volumeClaimTemplates": []any{claim("data-volume", d.Spec.StorageGiB), claim("logs-volume", database.MongoDBLogStorageGiB)},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{databaseOwner: d.ID}},
				"spec": map[string]any{
					"nodeSelector": map[string]any{corev1.LabelArchStable: "amd64"},
					"containers": []any{
						map[string]any{"name": "mongod", "image": mongodbServerImage, "resources": resources},
						map[string]any{"name": "mongodb-agent", "image": mongodbAgentImage, "resources": agent},
					},
					"initContainers": []any{
						map[string]any{"name": "mongod-posthook", "image": mongodbUpgradeImage, "resources": agent},
						map[string]any{"name": "mongodb-agent-readinessprobe", "image": mongodbReadinessImage, "resources": agent},
					},
				},
			},
		}},
	}
}

func safeMongoDBController(pod corev1.PodTemplateSpec) bool {
	if len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 0 {
		return false
	}
	container := pod.Spec.Containers[0]
	if container.Name != "mongodb-kubernetes-operator" || container.Image != mongodbControllerImage || len(container.Args) != 1 || container.Args[0] != "-watch-resource=mongodbcommunity" {
		return false
	}
	values := map[string]string{}
	for _, env := range container.Env {
		if env.ValueFrom == nil {
			values[env.Name] = env.Value
		}
	}
	return values["OPERATOR_ENV"] == "prod" && values["MDB_OPERATOR_TELEMETRY_ENABLED"] == "false" && values["MDB_MAX_CONCURRENT_RECONCILES"] == "1" && values["MDB_COMMUNITY_AGENT_IMAGE"] == mongodbAgentImage && values["READINESS_PROBE_IMAGE"] == mongodbReadinessImage && values["VERSION_UPGRADE_HOOK_IMAGE"] == mongodbUpgradeImage && !container.Resources.Limits.Cpu().IsZero() && !container.Resources.Limits.Memory().IsZero()
}

func (c *Client) prepareMongoDBSecurity(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.Engine != "mongodb" {
		return nil
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("MongoDB namespace ownership changed")
	}
	for _, name := range []string{"database-monitor", "database-recovery"} {
		secret, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			password := make([]byte, 32)
			if _, err = rand.Read(password); err != nil {
				return err
			}
			secret = &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, name), Immutable: ptr(true), Data: map[string][]byte{"password": []byte(hex.EncodeToString(password))}}
			if err = before(); err != nil {
				return err
			}
			secret, err = c.kube.CoreV1().Secrets(ns.Name).Create(ctx, secret, metav1.CreateOptions{})
		}
		if err != nil {
			return err
		}
		if err = databaseIdentityOwned(secret, d, ns.UID); err != nil {
			return err
		}
		if secret.Immutable == nil || !*secret.Immutable || len(secret.Data["password"]) != 64 {
			return fmt.Errorf("MongoDB internal credential identity changed")
		}
	}
	identity, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = databaseIdentityOwned(identity, d, ns.UID); err != nil {
		return err
	}
	cm, err := c.kube.CoreV1().ConfigMaps(ns.Name).Get(ctx, "database-ca-public", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = c.kube.CoreV1().ConfigMaps(ns.Name).Create(ctx, &corev1.ConfigMap{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-ca-public"), Data: map[string]string{"ca.crt": string(identity.Data["ca.crt"])}}, metav1.CreateOptions{})
	} else if err == nil {
		if !mongodbSupportOwned(cm, d, ns.UID) {
			return fmt.Errorf("MongoDB public trust ownership changed")
		}
		if cm.Data["ca.crt"] != string(identity.Data["ca.crt"]) {
			cm.Data = map[string]string{"ca.crt": string(identity.Data["ca.crt"])}
			if err = before(); err != nil {
				return err
			}
			_, err = c.kube.CoreV1().ConfigMaps(ns.Name).Update(ctx, cm, metav1.UpdateOptions{})
		}
	}
	if err != nil {
		return err
	}
	meta := databaseIdentityMeta(d, ns.UID, "mongodb-kubernetes-appdb")
	sa, err := c.kube.CoreV1().ServiceAccounts(ns.Name).Get(ctx, meta.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		sa, err = c.kube.CoreV1().ServiceAccounts(ns.Name).Create(ctx, &corev1.ServiceAccount{ObjectMeta: meta}, metav1.CreateOptions{})
	}
	if err != nil {
		return err
	}
	if !mongodbSupportOwned(sa, d, ns.UID) {
		return fmt.Errorf("MongoDB service account ownership changed")
	}
	names := make([]string, 7)
	for i := range names {
		names[i] = fmt.Sprintf("database-%d", i)
	}
	rules := []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{"database-config"}, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, ResourceNames: names, Verbs: []string{"get", "patch", "delete"}},
	}
	role, err := c.kube.RbacV1().Roles(ns.Name).Get(ctx, meta.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		role, err = c.kube.RbacV1().Roles(ns.Name).Create(ctx, &rbacv1.Role{ObjectMeta: meta, Rules: rules}, metav1.CreateOptions{})
	}
	if err != nil {
		return err
	}
	if !mongodbSupportOwned(role, d, ns.UID) || !reflect.DeepEqual(role.Rules, rules) {
		return fmt.Errorf("MongoDB agent permissions changed")
	}
	ref := rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: meta.Name}
	subjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: meta.Name, Namespace: ns.Name}}
	binding, err := c.kube.RbacV1().RoleBindings(ns.Name).Get(ctx, meta.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		binding, err = c.kube.RbacV1().RoleBindings(ns.Name).Create(ctx, &rbacv1.RoleBinding{ObjectMeta: meta, RoleRef: ref, Subjects: subjects}, metav1.CreateOptions{})
	}
	if err != nil {
		return err
	}
	if !mongodbSupportOwned(binding, d, ns.UID) || binding.RoleRef != ref || !reflect.DeepEqual(binding.Subjects, subjects) {
		return fmt.Errorf("MongoDB agent role binding changed")
	}
	return nil
}

func mongodbSupportOwned(object metav1.Object, d database.Resource, uid types.UID) bool {
	if object.GetDeletionTimestamp() != nil || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" || object.GetNamespace() != DatabaseNamespace(d.ID) {
		return false
	}
	for _, owner := range object.GetOwnerReferences() {
		if owner.APIVersion == "v1" && owner.Kind == "Namespace" && owner.UID == uid && owner.Name == object.GetNamespace() {
			return true
		}
	}
	return false
}

func mongodbPodImagesMatch(pod corev1.Pod, d database.Resource) bool {
	if len(pod.Spec.Containers) != 2 || len(pod.Spec.InitContainers) != 2 {
		return false
	}
	expected := map[string]string{"mongod": mongodbServerImage, "mongodb-agent": mongodbAgentImage, "mongod-posthook": mongodbUpgradeImage, "mongodb-agent-readinessprobe": mongodbReadinessImage}
	for _, container := range append(append([]corev1.Container{}, pod.Spec.Containers...), pod.Spec.InitContainers...) {
		if container.Image != expected[container.Name] {
			return false
		}
		delete(expected, container.Name)
		cpu, memory := database.MongoDBAgentCPU, database.MongoDBAgentMemory
		if container.Name == "mongod" {
			cpu, memory = d.Spec.CPU, d.Spec.Memory
		}
		for key, value := range map[corev1.ResourceName]string{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory} {
			wanted := resource.MustParse(value)
			request, limit := container.Resources.Requests[key], container.Resources.Limits[key]
			if request.Cmp(wanted) != 0 || limit.Cmp(wanted) != 0 {
				return false
			}
		}
	}
	return len(expected) == 0
}
