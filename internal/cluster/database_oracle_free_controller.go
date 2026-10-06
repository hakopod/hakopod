package cluster

import (
	"context"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// Filled only after the source-tested operator is published and anonymously
// pulled for native qualification. An empty pin cannot create any resources.
const oracleFreeOperatorImage = "ghcr.io/hakopod/managed-oracle-free-operator@sha256:ce6a767e9364a97d0c4bb342f0c2a621a59ecf230dabbd5517c06c88884d0471"
const oracleFreeControllerLabel = "hakopod.io/oracle-free-controller"

func validateOracleFreeOperatorImage() error {
	name, digest, ok := strings.Cut(oracleFreeOperatorImage, "@sha256:")
	value, err := hex.DecodeString(digest)
	if !ok || name == "" || err != nil || len(value) != 32 {
		return fmt.Errorf("Oracle Free operator image is awaiting native qualification")
	}
	return nil
}

func oracleFreeControllerObject(d database.Resource, namespaceUID types.UID) *unstructured.Unstructured {
	ns := DatabaseNamespace(d.ID)
	labels := map[string]any{managedBy: "hakopod", databaseOwner: d.ID, oracleFreeControllerLabel: "true"}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "oracle-free-operator", "namespace": ns, "labels": labels, "ownerReferences": []any{map[string]any{"apiVersion": "v1", "kind": "Namespace", "name": ns, "uid": string(namespaceUID)}}},
		"spec": map[string]any{"replicas": int64(1), "strategy": map[string]any{"type": "Recreate"}, "selector": map[string]any{"matchLabels": labels}, "template": map[string]any{
			"metadata": map[string]any{"labels": labels, "annotations": map[string]any{"hakopod.io/oracle-controller-source": oracleEnterpriseControllerSource, "hakopod.io/oracle-security-policy": oracleFreePolicy}},
			"spec": map[string]any{"serviceAccountName": "database-oracle-free-operator", "automountServiceAccountToken": true,
				"nodeSelector":    map[string]any{corev1.LabelArchStable: "amd64"},
				"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": int64(65532), "runAsGroup": int64(65532), "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
				"containers": []any{map[string]any{"name": "manager", "image": oracleFreeOperatorImage, "command": []any{"/manager"}, "args": []any{"--enable-leader-election=false", "--metrics-addr=0"},
					"env":             []any{map[string]any{"name": "WATCH_NAMESPACE", "value": ns}, map[string]any{"name": "ENABLE_WEBHOOKS", "value": "false"}, map[string]any{"name": "HAKOPOD_ORACLE_POLICY", "value": oracleFreePolicy}},
					"resources":       map[string]any{"requests": map[string]any{"cpu": database.OracleFreeOperatorCPU, "memory": database.OracleFreeOperatorMemory, "ephemeral-storage": "64Mi"}, "limits": map[string]any{"cpu": database.OracleFreeOperatorCPU, "memory": database.OracleFreeOperatorMemory, "ephemeral-storage": "128Mi"}},
					"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []any{"ALL"}}},
					"readinessProbe":  map[string]any{"httpGet": map[string]any{"path": "/readyz", "port": int64(8081), "scheme": "HTTP"}, "periodSeconds": int64(5), "timeoutSeconds": int64(3), "failureThreshold": int64(1)},
				}},
			},
		}},
	}}
}

func oracleFreeRoleRules() []any {
	rule := func(group string, resources, verbs []any) any {
		return map[string]any{"apiGroups": []any{group}, "resources": resources, "verbs": verbs}
	}
	write := []any{"get", "list", "watch", "create", "update", "patch", "delete"}
	return []any{
		rule("", []any{"pods", "services", "persistentvolumeclaims", "events", "configmaps"}, write),
		rule("", []any{"secrets"}, []any{"get", "list", "watch"}),
		rule("", []any{"pods/exec"}, []any{"create"}),
		rule("database.oracle.com", []any{"singleinstancedatabases", "singleinstancedatabases/status", "singleinstancedatabases/finalizers"}, []any{"get", "list", "watch", "update", "patch"}),
	}
}

func (c *Client) prepareOracleFreeController(ctx context.Context, d database.Resource, before func() error) error {
	if err := validateOracleFreeOperatorImage(); err != nil {
		return err
	}
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		return err
	}
	metadata := func(name string) map[string]any {
		return map[string]any{"name": name, "namespace": ns.Name, "labels": map[string]any{databaseOwner: d.ID, managedBy: "hakopod"}, "ownerReferences": []any{map[string]any{"apiVersion": "v1", "kind": "Namespace", "name": ns.Name, "uid": string(ns.UID)}}}
	}
	items := []struct {
		gvr    schema.GroupVersionResource
		object map[string]any
	}{
		{schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}, map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": metadata("database-oracle-free-operator"), "automountServiceAccountToken": true}},
		{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}, map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": metadata("database-oracle-free-operator"), "rules": oracleFreeRoleRules()}},
		{schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}, map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": metadata("database-oracle-free-operator"), "roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "database-oracle-free-operator"}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": "database-oracle-free-operator", "namespace": ns.Name}}}},
	}
	for _, item := range items {
		if err = c.applyVitessOwnedObject(ctx, d, ns.UID, item.gvr, &unstructured.Unstructured{Object: item.object}, before); err != nil {
			return err
		}
	}
	object, err := c.oracleFreeControllerDesired(ctx, d, ns.UID)
	if err != nil {
		return err
	}
	if err = c.prepareOracleFreeControllerNetwork(ctx, d, before); err != nil {
		return err
	}
	return c.applyVitessOwnedObject(ctx, d, ns.UID, schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, object, before)
}

func (c *Client) oracleFreeControllerDesired(ctx context.Context, d database.Resource, namespaceUID types.UID) (*unstructured.Unstructured, error) {
	object := oracleFreeControllerObject(d, namespaceUID)
	p, err := c.databasePolicy(ctx, d)
	if err != nil {
		return nil, err
	}
	if p != nil {
		_ = unstructured.SetNestedField(object.Object, p.RuntimeClass, "spec", "template", "spec", "runtimeClassName")
		_ = unstructured.SetNestedStringMap(object.Object, map[string]string{"hakopod.com/pool": p.Pool, DatabaseDefaultRuntimeLabel: p.RuntimeClass, corev1.LabelArchStable: "amd64"}, "spec", "template", "spec", "nodeSelector")
		_ = unstructured.SetNestedSlice(object.Object, []any{map[string]any{"key": "hakopod.com/pool", "operator": "Equal", "value": p.Pool, "effect": "NoSchedule"}}, "spec", "template", "spec", "tolerations")
	}
	nodes := d.Spec.Placement.NodeNames
	if len(nodes) == 0 && p != nil {
		nodes = p.nodes()
	}
	if len(nodes) > 0 {
		applyDatabasePlacement(object, d.Spec, nodes)
	}
	return object, nil
}

func (c *Client) prepareOracleFreeControllerNetwork(ctx context.Context, d database.Resource, before func() error) error {
	rules, err := c.databaseAPIEgress(ctx)
	if err != nil {
		return err
	}
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		return err
	}
	desired := &networkingv1.NetworkPolicy{ObjectMeta: databaseIdentityMeta(d, ns.UID, "oracle-free-controller"), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{oracleFreeControllerLabel: "true"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}, Egress: rules}}
	api := c.kube.NetworkingV1().NetworkPolicies(ns.Name)
	current, err := api.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = api.Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if !mongodbSupportOwned(current, d, ns.UID) {
		return fmt.Errorf("Oracle Free controller network policy ownership changed")
	}
	current.Spec = desired.Spec
	if err = before(); err != nil {
		return err
	}
	_, err = api.Update(ctx, current, metav1.UpdateOptions{})
	return err
}

func (c *Client) oracleFreeControllerReady(ctx context.Context, d database.Resource) error {
	// Admission checks the publication pin. Observation also requires the exact
	// desired image through full template and PodSpec equality below.
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		return err
	}
	if err = c.oracleFreeControllerAccessReady(ctx, d, ns.UID); err != nil {
		return err
	}
	deployment, err := c.kube.AppsV1().Deployments(ns.Name).Get(ctx, "oracle-free-operator", metav1.GetOptions{})
	if err != nil || deployment.UID == "" || deployment.DeletionTimestamp != nil || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 || deployment.Status.ObservedGeneration < deployment.Generation || deployment.Status.Replicas != 1 || deployment.Status.UpdatedReplicas != 1 || deployment.Status.AvailableReplicas != 1 {
		return fmt.Errorf("Oracle Free operator has no current available deployment")
	}
	if len(deployment.OwnerReferences) != 1 || deployment.OwnerReferences[0].APIVersion != "v1" || deployment.OwnerReferences[0].Kind != "Namespace" || deployment.OwnerReferences[0].Name != ns.Name || deployment.OwnerReferences[0].UID != ns.UID || deployment.Labels[databaseOwner] != d.ID || deployment.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Oracle Free operator ownership changed")
	}
	wanted, err := c.oracleFreeControllerDesired(ctx, d, ns.UID)
	if err != nil {
		return err
	}
	var desired appsv1.Deployment
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(wanted.Object, &desired); err != nil {
		return err
	}
	if !reflect.DeepEqual(deployment.Spec.Strategy, desired.Spec.Strategy) || !reflect.DeepEqual(deployment.Spec.Selector, desired.Spec.Selector) {
		return fmt.Errorf("Oracle Free operator deployment policy changed")
	}
	expected := desired.Spec.Template
	if !reflect.DeepEqual(deployment.Spec.Template.Annotations, expected.Annotations) || !reflect.DeepEqual(deployment.Spec.Template.Labels, expected.Labels) || !oracleFreePodSpecsEqual(deployment.Spec.Template.Spec, expected.Spec) {
		return fmt.Errorf("Oracle Free operator template changed")
	}
	pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: oracleFreeControllerLabel + "=true", Limit: 3})
	if err != nil || pods.Continue != "" || len(pods.Items) != 1 {
		return fmt.Errorf("Oracle Free operator member inventory changed")
	}
	pod := pods.Items[0]
	owner := metav1.GetControllerOf(&pod)
	if pod.UID == "" || pod.DeletionTimestamp != nil || len(pod.OwnerReferences) != 1 || owner == nil || owner.APIVersion != "apps/v1" || owner.Kind != "ReplicaSet" || pod.Labels[databaseOwner] != d.ID || pod.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Oracle Free operator pod ownership changed")
	}
	rs, err := c.kube.AppsV1().ReplicaSets(ns.Name).Get(ctx, owner.Name, metav1.GetOptions{})
	if err != nil || rs.UID != owner.UID || rs.DeletionTimestamp != nil {
		return fmt.Errorf("Oracle Free operator ReplicaSet changed")
	}
	parent := metav1.GetControllerOf(rs)
	if len(rs.OwnerReferences) != 1 || parent == nil || parent.Kind != "Deployment" || parent.APIVersion != "apps/v1" || parent.Name != deployment.Name || parent.UID != deployment.UID {
		return fmt.Errorf("Oracle Free operator deployment identity changed")
	}
	if !oracleFreeControllerPodMatches(pod, expected.Spec) {
		return fmt.Errorf("Oracle Free operator pod policy changed")
	}
	ready := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		return fmt.Errorf("Oracle Free operator pod is not ready")
	}
	return nil
}

func (c *Client) oracleFreeControllerAccessReady(ctx context.Context, d database.Resource, namespaceUID types.UID) error {
	ns, name := DatabaseNamespace(d.ID), "database-oracle-free-operator"
	owned := func(object metav1.Object) bool {
		return object.GetUID() != "" && len(object.GetOwnerReferences()) == 1 && mongodbSupportOwned(object, d, namespaceUID)
	}
	account, err := c.kube.CoreV1().ServiceAccounts(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil || !owned(account) || account.AutomountServiceAccountToken == nil || !*account.AutomountServiceAccountToken || len(account.Secrets) != 0 || len(account.ImagePullSecrets) != 0 {
		return fmt.Errorf("Oracle Free operator service account changed")
	}
	role, err := c.kube.RbacV1().Roles(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil || !owned(role) {
		return fmt.Errorf("Oracle Free operator role identity changed")
	}
	var desired rbacv1.Role
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(map[string]any{"rules": oracleFreeRoleRules()}, &desired); err != nil {
		return err
	}
	if !reflect.DeepEqual(role.Rules, desired.Rules) {
		return fmt.Errorf("Oracle Free operator permissions changed")
	}
	binding, err := c.kube.RbacV1().RoleBindings(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil || !owned(binding) || binding.RoleRef != (rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: name}) || !reflect.DeepEqual(binding.Subjects, []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: ns}}) {
		return fmt.Errorf("Oracle Free operator permission binding changed")
	}
	return nil
}

func oracleFreeControllerPodMatches(pod corev1.Pod, expected corev1.PodSpec) bool {
	actual := pod.Spec.DeepCopy()
	// The controller alone receives Kubernetes' standard projected token.
	// Database members cannot enter this normalization path.
	if actual.AutomountServiceAccountToken == nil || !*actual.AutomountServiceAccountToken || actual.ServiceAccountName != "database-oracle-free-operator" || len(actual.Containers) != 1 {
		return false
	}
	if actual.DeprecatedServiceAccount == actual.ServiceAccountName {
		actual.DeprecatedServiceAccount = ""
	}
	if len(actual.Volumes) != 1 {
		return false
	}
	volume := actual.Volumes[0]
	if !strings.HasPrefix(volume.Name, "kube-api-access-") || volume.Projected == nil || len(volume.Projected.Sources) != 3 || volume.Projected.DefaultMode == nil || *volume.Projected.DefaultMode != 0644 {
		return false
	}
	token, ca, namespace := volume.Projected.Sources[0], volume.Projected.Sources[1], volume.Projected.Sources[2]
	if token.ServiceAccountToken == nil || token.ServiceAccountToken.Path != "token" || token.ServiceAccountToken.Audience != "" || token.ServiceAccountToken.ExpirationSeconds == nil || *token.ServiceAccountToken.ExpirationSeconds < 3600 || *token.ServiceAccountToken.ExpirationSeconds > 3607 {
		return false
	}
	if ca.ConfigMap == nil || ca.ConfigMap.Name != "kube-root-ca.crt" || len(ca.ConfigMap.Items) != 1 || ca.ConfigMap.Items[0].Key != "ca.crt" || ca.ConfigMap.Items[0].Path != "ca.crt" {
		return false
	}
	if namespace.DownwardAPI == nil || len(namespace.DownwardAPI.Items) != 1 || namespace.DownwardAPI.Items[0].Path != "namespace" || namespace.DownwardAPI.Items[0].FieldRef == nil || namespace.DownwardAPI.Items[0].FieldRef.FieldPath != "metadata.namespace" {
		return false
	}
	mounts := actual.Containers[0].VolumeMounts
	if len(mounts) != 1 || mounts[0].Name != volume.Name || mounts[0].MountPath != "/var/run/secrets/kubernetes.io/serviceaccount" || !mounts[0].ReadOnly || mounts[0].SubPath != "" || mounts[0].SubPathExpr != "" {
		return false
	}
	actual.Volumes, actual.Containers[0].VolumeMounts = nil, nil
	return oracleFreePodSpecsEqual(*actual, expected)
}
