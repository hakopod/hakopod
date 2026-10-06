package cluster

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

const oracleFreePolicy = "hakopod-oracle-free-tcps-v1"
const oracleFreePodPolicy = "hakopod.io/oracle-free-pod-policy"
const oracleFreeIdentity = "hakopod.io/oracle-identity"

func oracleFreeObject(d database.Resource, p *DatabasePolicy, nodes []string) (*unstructured.Unstructured, error) {
	if err := d.Spec.Validate(); err != nil {
		return nil, err
	}
	if err := oracleRuntimeSupported(d.Spec); err != nil {
		return nil, err
	}
	resources := map[string]any{"requests": map[string]any{"cpu": d.Spec.CPU, "memory": d.Spec.Memory}, "limits": map[string]any{"cpu": d.Spec.CPU, "memory": d.Spec.Memory}}
	workload := &unstructured.Unstructured{Object: map[string]any{"spec": oracleFreeWorkloadSpec(d, resources)}}
	if p != nil {
		applyDatabasePolicy(workload, d.Spec, *p)
	}
	applyDatabasePlacement(workload, d.Spec, nodes)
	pod, _, _ := unstructured.NestedMap(workload.Object, "spec", "template", "spec")
	containers := pod["containers"].([]any)
	containers[0].(map[string]any)["name"] = "database"
	for _, raw := range containers[0].(map[string]any)["volumeMounts"].([]any) {
		mount := raw.(map[string]any)
		if mount["name"] == "backup" {
			mount["name"] = "additional-pvc-0"
		}
	}
	volumes := pod["volumes"].([]any)
	for _, name := range []string{"data", "backup"} {
		volumeName, claimName := "data", "database"
		if name == "backup" {
			volumeName, claimName = "additional-pvc-0", "database-additional-0"
		}
		volumes = append(volumes, map[string]any{"name": volumeName, "persistentVolumeClaim": map[string]any{"claimName": claimName}})
	}
	pod["volumes"] = volumes
	selector, _ := pod["nodeSelector"].(map[string]any)
	if selector == nil {
		selector = map[string]any{}
	}
	selector[corev1.LabelArchStable] = "amd64"
	pod["nodeSelector"] = selector
	envelope, err := json.Marshal(map[string]any{"profile": oracleFreePolicy, "podSpec": pod})
	if err != nil || len(envelope) > 32<<10 {
		return nil, fmt.Errorf("Oracle Free pod policy exceeds its bound")
	}
	storage := map[string]any{"size": fmt.Sprintf("%dGi", d.Spec.StorageGiB), "accessMode": "ReadWriteOnce"}
	backup := map[string]any{"mountPath": "/opt/oracle/hakopod-backup", "storageSizeInGb": d.Spec.StorageGiB}
	if p != nil {
		storage["storageClass"] = p.StorageClass
		backup["storageClass"] = p.StorageClass
	}
	labels := map[string]any{}
	for key, value := range databaseLabels(d) {
		labels[key] = value
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "database.oracle.com/v4", "kind": "SingleInstanceDatabase",
		"metadata": map[string]any{"name": "database", "namespace": DatabaseNamespace(d.ID), "labels": labels, "annotations": map[string]any{"hakopod.io/database-revision": strconv.FormatInt(d.Revision, 10), oracleFreePodPolicy: string(envelope)}},
		"spec": map[string]any{
			"edition": "free", "sid": "FREE", "pdbName": "FREEPDB1", "charset": "AL32UTF8", "createAs": "primary", "replicas": int64(1),
			"image":     map[string]any{"pullFrom": database.OracleFreeImage, "prebuiltDB": true, "imagePullPolicy": "IfNotPresent"},
			"resources": resources, "shmSize": "1Gi", "disableDefaultDiagVolumeClaim": true, "automountServiceAccountToken": false,
			"security":    map[string]any{"secrets": map[string]any{"admin": map[string]any{"secretName": "database-oracle-access", "secretKey": "admin", "keepSecret": true}}, "tcps": map[string]any{"enabled": true, "tlsSecret": "database-tls"}},
			"services":    map[string]any{"endpoints": []any{map[string]any{"name": "cluster", "type": "ClusterIP", "tcp": map[string]any{"enabled": false}, "tcps": map[string]any{"enabled": true, "port": int64(2484)}}}},
			"persistence": map[string]any{"oradata": storage, "additionalPVCs": []any{backup}, "setWritePermissions": false},
			"archiveLog":  false, "forceLog": false, "flashBack": false,
			"dataguard": map[string]any{"mode": "Disabled", "prereqs": map[string]any{"enabled": false}},
		},
	}}, nil
}

// Admission remains in ApplyDatabase. Native qualification calls this same
// implementation from package tests; it does not add a production gate bypass.
func (c *Client) applyOracleFreeDatabase(ctx context.Context, d database.Resource, password []byte, before func() error) error {
	if err := d.Spec.Validate(); err != nil {
		return err
	}
	if err := oracleRuntimeSupported(d.Spec); err != nil {
		return err
	}
	if len(password) != 64 {
		return fmt.Errorf("Oracle application credential is invalid")
	}
	if _, err := hex.DecodeString(string(password)); err != nil {
		return fmt.Errorf("Oracle application credential is invalid")
	}
	if err := validateOracleFreeOperatorImage(); err != nil {
		return err
	}
	if err := c.ValidateDatabasePlacement(ctx, d); err != nil {
		return err
	}
	object, err := c.databaseObject(ctx, d)
	if err != nil {
		return err
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		labels := databaseLabels(d)
		labels["pod-security.kubernetes.io/enforce"] = "restricted"
		if err = before(); err != nil {
			return err
		}
		ns, err = c.kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), Labels: labels}}, metav1.CreateOptions{})
	}
	if err != nil {
		return err
	}
	if ns, err = c.oracleEnterpriseNamespace(ctx, d); err != nil {
		return err
	}
	// Existing StatefulSets are never adopted in place. Native storage migration
	// requires a separate, inspected restore target.
	if old, e := c.kube.AppsV1().StatefulSets(ns.Name).Get(ctx, "database", metav1.GetOptions{}); e == nil && old != nil {
		return fmt.Errorf("Oracle Free requires a separate operator-managed restore target")
	} else if e != nil && !apierrors.IsNotFound(e) {
		return e
	}
	if err = c.oracleFreeResourcesPreflight(ctx, d); err != nil {
		return err
	}
	api := c.kube.CoreV1().Secrets(ns.Name)
	secret, err := api.Get(ctx, "database-credentials", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		secret = &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-credentials"), Type: corev1.SecretTypeBasicAuth, Immutable: ptr(true), Data: map[string][]byte{"username": []byte("app"), "password": password}}
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
	if secret.Immutable == nil || !*secret.Immutable || string(secret.Data["username"]) != "app" || subtle.ConstantTimeCompare(secret.Data["password"], password) != 1 {
		return fmt.Errorf("Oracle application credential identity changed")
	}
	if err = c.databaseNetworkPolicy(ctx, d, before); err != nil {
		return err
	}
	if err = c.prepareDatabaseIdentity(ctx, d, before); err != nil {
		return err
	}
	if err = c.prepareOracleFreeController(ctx, d, before); err != nil {
		return err
	}
	if err = c.prepareOracleSecurity(ctx, d, before); err != nil {
		return err
	}
	identity, err := api.Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil {
		return err
	}
	annotations := object.GetAnnotations()
	annotations[oracleFreeIdentity] = oracleIdentityFingerprint(identity)
	object.SetAnnotations(annotations)
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}})
	current, err := c.dynamic.Resource(oracleDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = c.dynamic.Resource(oracleDatabaseResource).Namespace(ns.Name).Create(ctx, object, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if err = c.oracleEnterpriseObjectOwned(ctx, d, current); err != nil {
		return err
	}
	if !oracleFreeSpecMatches(current, object) {
		return fmt.Errorf("Oracle Free controller specification changed; use a separate reviewed target")
	}
	return nil
}

func oracleFreeSpecMatches(current, wanted *unstructured.Unstructured) bool {
	actual, _, _ := unstructured.NestedMap(current.Object, "spec")
	desired, _, _ := unstructured.NestedMap(wanted.Object, "spec")
	return reflect.DeepEqual(actual, desired) && current.GetAnnotations()[oracleFreePodPolicy] == wanted.GetAnnotations()[oracleFreePodPolicy]
}

func (c *Client) renewOracleFreeWorkload(ctx context.Context, d database.Resource, before func() error) error {
	api := c.dynamic.Resource(oracleDatabaseResource).Namespace(DatabaseNamespace(d.ID))
	object, err := api.Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = c.oracleEnterpriseObjectOwned(ctx, d, object); err != nil {
		return err
	}
	desired, err := c.databaseObject(ctx, d)
	if err != nil {
		return err
	}
	identity, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil {
		return err
	}
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		return err
	}
	if err = databaseIdentityOwned(identity, d, ns.UID); err != nil {
		return err
	}
	fingerprint := oracleIdentityFingerprint(identity)
	annotations := object.GetAnnotations()
	if annotations[oracleFreeIdentity] != fingerprint || annotations[oracleFreePodPolicy] != desired.GetAnnotations()[oracleFreePodPolicy] {
		annotations[oracleFreeIdentity] = fingerprint
		annotations[oracleFreePodPolicy] = desired.GetAnnotations()[oracleFreePodPolicy]
		object.SetAnnotations(annotations)
		if err = before(); err != nil {
			return err
		}
		if _, err = api.Update(ctx, object, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	// SIDB owns Pods directly and does not roll on arbitrary annotations.
	// Retire only the obsolete owned singleton. The controller recreates it.
	pods, err := c.kube.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: oracleEnterpriseMemberLabel + "=true", Limit: 2})
	if err != nil {
		return err
	}
	if pods.Continue != "" || len(pods.Items) > 1 {
		return fmt.Errorf("Oracle Free member replacement has not converged")
	}
	for _, pod := range pods.Items {
		owner := metav1.GetControllerOf(&pod)
		if owner == nil || owner.APIVersion != "database.oracle.com/v4" || owner.Kind != "SingleInstanceDatabase" || owner.Name != object.GetName() || owner.UID != object.GetUID() || pod.Labels[databaseOwner] != d.ID {
			return fmt.Errorf("Oracle Free member ownership changed")
		}
		if pod.DeletionTimestamp != nil {
			return nil
		}
		if pod.Annotations[oracleFreeIdentity] == fingerprint && pod.Annotations[oracleFreePodPolicy] == desired.GetAnnotations()[oracleFreePodPolicy] {
			continue
		}
		if err = c.oracleFreeControllerReady(ctx, d); err != nil {
			return err
		}
		if err = before(); err != nil {
			return err
		}
		if err = c.kube.CoreV1().Pods(ns.Name).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}}); err != nil {
			return err
		}
	}
	return nil
}

func oracleFreePodMatches(pod corev1.Pod, d database.Resource) bool {
	if len(pod.Spec.Containers) != 1 || len(pod.Spec.InitContainers) != 0 || pod.Spec.Containers[0].Name != "database" || pod.Spec.Containers[0].Image != database.OracleFreeImage || pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken || pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC {
		return false
	}
	want, err := oracleFreeObject(d, nil, d.Spec.Placement.NodeNames)
	if err != nil {
		return false
	}
	var envelope struct {
		PodSpec corev1.PodSpec `json:"podSpec"`
	}
	if json.Unmarshal([]byte(want.GetAnnotations()[oracleFreePodPolicy]), &envelope) != nil {
		return false
	}
	actual := pod.Spec.DeepCopy()
	// Placement is checked against the server's allocation independently. It
	// is not part of the edition's invariant bootstrap and secret contract.
	if actual.NodeSelector[corev1.LabelArchStable] != "amd64" {
		return false
	}
	if actual.RuntimeClassName != nil && *actual.RuntimeClassName == "runsc" {
		if _, _, valid := managedPlatformSandboxOverhead(actual.Overhead); !valid {
			return false
		}
		actual.Overhead = nil
	}
	actual.Affinity, actual.NodeSelector, actual.Tolerations, actual.RuntimeClassName = nil, nil, nil, nil
	envelope.PodSpec.Affinity, envelope.PodSpec.NodeSelector, envelope.PodSpec.Tolerations, envelope.PodSpec.RuntimeClassName = nil, nil, nil, nil
	return oracleFreePodSpecsEqual(*actual, envelope.PodSpec)
}

// Permit only Kubernetes defaults and the scheduler's assigned node. Every
// workload field that can change credentials, commands, mounts or privileges
// remains in the comparison, including arguments, environment and lifecycle.
func oracleFreePodSpecsEqual(actual, expected corev1.PodSpec) bool {
	normalize := func(spec *corev1.PodSpec) {
		spec.NodeName = ""
		if spec.RuntimeClassName != nil && *spec.RuntimeClassName == "runsc" {
			if _, _, valid := managedPlatformSandboxOverhead(spec.Overhead); valid {
				spec.Overhead = nil
			}
		}
		if spec.ServiceAccountName == "default" {
			spec.ServiceAccountName = ""
		}
		if spec.DeprecatedServiceAccount == "default" {
			spec.DeprecatedServiceAccount = ""
		}
		if spec.RestartPolicy == "" {
			spec.RestartPolicy = corev1.RestartPolicyAlways
		}
		if spec.DNSPolicy == "" {
			spec.DNSPolicy = corev1.DNSClusterFirst
		}
		if spec.SchedulerName == "" {
			spec.SchedulerName = "default-scheduler"
		}
		if spec.EnableServiceLinks == nil {
			spec.EnableServiceLinks = ptr(true)
		}
		if spec.Priority == nil {
			spec.Priority = ptr(int32(0))
		}
		if spec.PreemptionPolicy == nil {
			value := corev1.PreemptLowerPriority
			spec.PreemptionPolicy = &value
		}
		if spec.TerminationGracePeriodSeconds == nil {
			spec.TerminationGracePeriodSeconds = ptr(int64(30))
		}
		spec.Tolerations = slices.DeleteFunc(spec.Tolerations, func(value corev1.Toleration) bool {
			return (value.Key == "node.kubernetes.io/not-ready" || value.Key == "node.kubernetes.io/unreachable") && value.Operator == corev1.TolerationOpExists && value.Effect == corev1.TaintEffectNoExecute && value.Value == "" && value.TolerationSeconds != nil && *value.TolerationSeconds == 300
		})
		if len(spec.Tolerations) == 0 {
			spec.Tolerations = nil
		}
		for i := range spec.Containers {
			container := &spec.Containers[i]
			if container.ImagePullPolicy == "" {
				container.ImagePullPolicy = corev1.PullIfNotPresent
			}
			if container.TerminationMessagePath == "" {
				container.TerminationMessagePath = "/dev/termination-log"
			}
			if container.TerminationMessagePolicy == "" {
				container.TerminationMessagePolicy = corev1.TerminationMessageReadFile
			}
			for j := range container.Ports {
				if container.Ports[j].Protocol == "" {
					container.Ports[j].Protocol = corev1.ProtocolTCP
				}
			}
			for _, probe := range []*corev1.Probe{container.StartupProbe, container.ReadinessProbe, container.LivenessProbe} {
				if probe == nil {
					continue
				}
				if probe.TimeoutSeconds == 0 {
					probe.TimeoutSeconds = 1
				}
				if probe.PeriodSeconds == 0 {
					probe.PeriodSeconds = 10
				}
				if probe.SuccessThreshold == 0 {
					probe.SuccessThreshold = 1
				}
				if probe.FailureThreshold == 0 {
					probe.FailureThreshold = 3
				}
			}
		}
	}
	left, right := actual.DeepCopy(), expected.DeepCopy()
	normalize(left)
	normalize(right)
	return reflect.DeepEqual(left, right)
}

func (c *Client) oracleFreeExecTarget(ctx context.Context, d database.Resource, member database.Member) (*corev1.Pod, string, error) {
	object, err := c.dynamic.Resource(oracleDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		return nil, "", err
	}
	if err = c.oracleEnterpriseObjectOwned(ctx, d, object); err != nil {
		return nil, "", err
	}
	pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).Get(ctx, member.Name, metav1.GetOptions{})
	if err != nil || pod.UID == "" || pod.UID != types.UID(member.UID) || pod.DeletionTimestamp != nil || !oracleFreePodMatches(*pod, d) {
		return nil, "", fmt.Errorf("Oracle Free execution target changed")
	}
	owner := metav1.GetControllerOf(pod)
	if len(pod.OwnerReferences) != 1 || owner == nil || owner.Kind != "SingleInstanceDatabase" || owner.APIVersion != "database.oracle.com/v4" || owner.Name != "database" || owner.UID != object.GetUID() || pod.Labels[databaseOwner] != d.ID || pod.Labels[managedBy] != "hakopod" || pod.Labels[oracleEnterpriseMemberLabel] != "true" {
		return nil, "", fmt.Errorf("Oracle Free member ownership changed")
	}
	wanted, err := c.databaseObject(ctx, d)
	if err != nil {
		return nil, "", err
	}
	var envelope struct {
		PodSpec corev1.PodSpec `json:"podSpec"`
	}
	if !oracleFreeSpecMatches(object, wanted) || json.Unmarshal([]byte(wanted.GetAnnotations()[oracleFreePodPolicy]), &envelope) != nil || !oracleFreePodSpecsEqual(pod.Spec, envelope.PodSpec) {
		return nil, "", fmt.Errorf("Oracle Free execution policy changed")
	}
	if err = c.oracleFreeClaimsOwned(ctx, d, object); err != nil {
		return nil, "", err
	}
	return pod, "database", nil
}

func (c *Client) oracleFreeRevisionReady(ctx context.Context, d database.Resource, object *unstructured.Unstructured) error {
	if err := c.oracleFreeControllerReady(ctx, d); err != nil {
		return err
	}
	if err := c.oracleEnterpriseObjectOwned(ctx, d, object); err != nil {
		return err
	}
	wanted, err := c.databaseObject(ctx, d)
	if err != nil {
		return err
	}
	if !oracleFreeSpecMatches(object, wanted) {
		return fmt.Errorf("Oracle Free controller policy has not converged")
	}
	identity, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil {
		return err
	}
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		return err
	}
	if err = databaseIdentityOwned(identity, d, ns.UID); err != nil {
		return err
	}
	if err = c.oracleFreeServiceOwned(ctx, d, object); err != nil {
		return err
	}
	if !oracleFreeStatusReady(object) {
		return fmt.Errorf("Oracle Free controller has not observed the current revision")
	}
	pods, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{LabelSelector: oracleEnterpriseMemberLabel + "=true", Limit: 2})
	if err != nil {
		return err
	}
	if pods.Continue != "" || len(pods.Items) != 1 {
		return fmt.Errorf("Oracle Free has no unique member")
	}
	pod := pods.Items[0]
	if pod.Annotations[oracleFreeIdentity] != oracleIdentityFingerprint(identity) || pod.Annotations[oracleFreePodPolicy] != wanted.GetAnnotations()[oracleFreePodPolicy] || pod.Annotations["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return fmt.Errorf("Oracle Free identity or revision has not converged")
	}
	_, _, err = c.oracleFreeExecTarget(ctx, d, database.Member{Name: pod.Name, UID: string(pod.UID)})
	return err
}

func oracleFreeStatusReady(object *unstructured.Unstructured) bool {
	status, _, _ := unstructured.NestedString(object.Object, "status", "status")
	replicas, _, _ := unstructured.NestedInt64(object.Object, "status", "replicas")
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	if status != "Healthy" || replicas != 1 || object.GetGeneration() < 1 || len(conditions) > 16 {
		return false
	}
	var complete, pending time.Time
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		generation, _ := condition["observedGeneration"].(int64)
		if generation != object.GetGeneration() || condition["status"] != "True" {
			continue
		}
		stamp, _ := condition["lastTransitionTime"].(string)
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return false
		}
		if condition["type"] == "ReconcileComplete" && condition["reason"] == "LastReconcileCycleCompleted" {
			complete = at
		} else if at.After(pending) {
			pending = at
		}
	}
	return !complete.IsZero() && (pending.IsZero() || complete.After(pending))
}
