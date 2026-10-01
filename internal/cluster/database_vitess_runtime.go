package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func (c *Client) applyDatabaseAttachments(ctx context.Context, d database.Resource, before func() error) error {
	if d.Spec.Engine == "vitess" {
		return c.reconcileVitessRuntime(ctx, d, before)
	}
	return c.applyDatabasePoolers(ctx, d, before)
}

func (c *Client) reconcileVitessRuntime(ctx context.Context, d database.Resource, before func() error) error {
	expectedIdentity, err := c.vitessIdentityFingerprint(ctx, d)
	if err != nil {
		return err
	}
	if err := c.reconcileVitessServices(ctx, d, before); err != nil {
		return err
	}
	pods, err := c.kube.CoreV1().Pods(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{LabelSelector: vitessComponentLabel + " in (tablet,control)", Limit: database.MaxMembers + 3, FieldSelector: activeDatabasePodFields})
	if err != nil || pods.Continue != "" || len(pods.Items) > database.MaxMembers+2 {
		return fmt.Errorf("Vitess bootstrap inventory exceeds its bound")
	}
	tablets, controls := vitessReadyIdentityCounts(pods.Items, expectedIdentity)
	if tablets != d.Spec.Members() || controls != 1 {
		return nil
	}
	return c.reconcileVitessRoutingWithIdentity(ctx, d, expectedIdentity, before)
}

func vitessPodIdentityMatches(pod corev1.Pod, expected string) bool {
	return expected != "" && pod.Annotations[vitessIdentityAnnotation] == expected
}

func vitessPodReadyWithIdentity(pod corev1.Pod, expected string) bool {
	if pod.DeletionTimestamp != nil || !vitessPodIdentityMatches(pod, expected) {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func vitessReadyIdentityCounts(pods []corev1.Pod, expected string) (tablets, controls int) {
	for _, pod := range pods {
		if !vitessPodReadyWithIdentity(pod, expected) {
			continue
		}
		switch pod.Labels[vitessComponentLabel] {
		case "tablet":
			tablets++
		case "control":
			controls++
		}
	}
	return tablets, controls
}

func vitessPodReady(pod corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// vitessTopologyRolloutTarget returns one stale voter only after every voter is
// ready. Sorting makes retries choose the same voter until its replacement has
// the current identity.
func vitessTopologyRolloutTarget(pods []corev1.Pod, expected string) (corev1.Pod, bool, error) {
	var empty corev1.Pod
	if len(expected) != 64 {
		return empty, false, fmt.Errorf("Vitess topology identity is invalid")
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return empty, false, fmt.Errorf("Vitess topology identity is invalid")
	}
	if len(pods) != 3 {
		return empty, false, nil
	}
	ordered := append([]corev1.Pod(nil), pods...)
	slices.SortFunc(ordered, func(a, b corev1.Pod) int { return strings.Compare(a.Name, b.Name) })
	for _, pod := range ordered {
		if !vitessPodReady(pod) {
			return empty, false, nil
		}
		identity := pod.Annotations[vitessIdentityAnnotation]
		if len(identity) != 64 {
			return empty, false, fmt.Errorf("Vitess topology voter identity is invalid")
		}
		if _, err := hex.DecodeString(identity); err != nil {
			return empty, false, fmt.Errorf("Vitess topology voter identity is invalid")
		}
	}
	for _, pod := range ordered {
		identity := pod.Annotations[vitessIdentityAnnotation]
		if identity != expected {
			return pod, false, nil
		}
	}
	return empty, true, nil
}

func (c *Client) vitessIdentityFingerprint(ctx context.Context, d database.Resource) (string, error) {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return "", fmt.Errorf("Vitess identity namespace ownership changed")
	}
	secret, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, "database-tls", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("Vitess identity is unavailable")
	}
	if err = databaseIdentityOwned(secret, d, ns.UID); err != nil {
		return "", err
	}
	certificate := secret.Data["tls.crt"]
	if len(certificate) == 0 || len(certificate) > 64<<10 {
		return "", fmt.Errorf("Vitess identity certificate is invalid")
	}
	return fmt.Sprintf("%x", sha256.Sum256(certificate)), nil
}

func (c *Client) applyVitessIdentity(ctx context.Context, d database.Resource, object *unstructured.Unstructured) error {
	fingerprint, err := c.vitessIdentityFingerprint(ctx, d)
	if err != nil {
		return err
	}
	mutateVitessComponents(object, func(item map[string]any, _ string) {
		item["annotations"] = map[string]any{vitessIdentityAnnotation: fingerprint}
	})
	return nil
}

func (c *Client) renewVitessIdentity(ctx context.Context, d database.Resource, before func() error) error {
	if err := c.ReconcileVitessBackupAuthority(ctx, d, before); err != nil {
		return err
	}
	if err := c.prepareDatabaseIdentity(ctx, d, before); err != nil {
		return err
	}
	api := c.dynamic.Resource(vitessDatabaseResource).Namespace(DatabaseNamespace(d.ID))
	current, err := api.Get(ctx, "database", metav1.GetOptions{})
	if err != nil || current.GetUID() == "" || current.GetLabels()[databaseOwner] != d.ID || current.GetDeletionTimestamp() != nil {
		return fmt.Errorf("Vitess identity controller ownership changed")
	}
	updated := current.DeepCopy()
	if err = c.applyVitessIdentity(ctx, d, updated); err != nil {
		return err
	}
	fingerprint, err := c.vitessIdentityFingerprint(ctx, d)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(updated.Object["spec"], current.Object["spec"]) {
		if err = before(); err != nil {
			return err
		}
		if _, err = api.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	if err = c.prepareVitessController(ctx, d, before); err != nil {
		return err
	}
	if err = c.reconcileVitessRuntime(ctx, d, before); err != nil {
		return err
	}
	return c.rollVitessTopologyIdentity(ctx, d, updated, fingerprint, before)
}

type vitessTopologyHealthCheck func(context.Context, database.Resource, *unstructured.Unstructured, *DatabasePolicy, corev1.Pod) error

func (c *Client) rollVitessTopologyIdentity(ctx context.Context, d database.Resource, object *unstructured.Unstructured, expectedIdentity string, before func() error) error {
	health := func(check context.Context, d database.Resource, object *unstructured.Unstructured, policy *DatabasePolicy, pod corev1.Pod) error {
		member := database.Member{Name: pod.Name, UID: string(pod.UID), Role: "topology", Node: pod.Spec.NodeName, Phase: string(pod.Status.Phase)}
		_, err := c.verifyVitessEtcdMemberWithExec(check, d, member, func(command []string, stdout io.Writer) error {
			current, getErr := c.kube.CoreV1().Pods(pod.Namespace).Get(check, pod.Name, metav1.GetOptions{})
			if getErr != nil || current.UID != pod.UID || !c.vitessPodOwned(check, *current, object.GetUID()) || current.Labels[databaseOwner] != d.ID || current.Labels[vitessComponentLabel] != "topology" || !vitessPodMatches(*current, d) || !databasePodPolicyMatches(*current, policy) || !vitessPodReady(*current) || current.Annotations[vitessIdentityAnnotation] != pod.Annotations[vitessIdentityAnnotation] {
				return fmt.Errorf("Vitess topology voter changed before its quorum check")
			}
			return c.databaseExecVerifiedPod(check, current, "etcd", command, nil, stdout)
		})
		return err
	}
	return c.rollVitessTopologyIdentityWithHealth(ctx, d, object, expectedIdentity, before, health)
}

// The pinned operator updates the EtcdLockserver template annotation but does
// not replace already-ready etcd Pods for an annotation-only change. Replace
// one owned voter at a time and prove that the replacement can commit before
// disrupting the next voter.
func (c *Client) rollVitessTopologyIdentityWithHealth(ctx context.Context, d database.Resource, object *unstructured.Unstructured, expectedIdentity string, before func() error, health vitessTopologyHealthCheck) error {
	if object == nil || object.GetUID() == "" || object.GetDeletionTimestamp() != nil || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess identity controller ownership changed")
	}
	if health == nil {
		return fmt.Errorf("Vitess topology health verification is unavailable")
	}
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || namespace.UID == "" || namespace.DeletionTimestamp != nil || namespace.Labels[databaseOwner] != d.ID || namespace.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess identity namespace ownership changed")
	}
	namespaceUID := namespace.UID
	policy, err := c.databasePolicy(ctx, d)
	if err != nil {
		return err
	}
	wait, stop := context.WithTimeout(ctx, 5*time.Minute)
	defer stop()
	ns := DatabaseNamespace(d.ID)
	etcdName := vitessGeneratedName("database", "etcd")
	for {
		lockserver, getErr := c.dynamic.Resource(schema.GroupVersionResource{Group: "planetscale.com", Version: "v2", Resource: "etcdlockservers"}).Namespace(ns).Get(wait, etcdName, metav1.GetOptions{})
		if getErr == nil {
			identity, found, nestedErr := unstructured.NestedString(lockserver.Object, "spec", "annotations", vitessIdentityAnnotation)
			if nestedErr != nil || !found {
				return fmt.Errorf("Vitess topology identity template is invalid")
			}
			if lockserver.GetUID() == "" || lockserver.GetDeletionTimestamp() != nil || !c.vitessOwnedChain(wait, ns, lockserver.GetUID(), lockserver.GetOwnerReferences(), object.GetUID()) {
				return fmt.Errorf("Vitess topology controller ownership changed")
			}
			if identity == expectedIdentity {
				pods, listErr := c.kube.CoreV1().Pods(ns).List(wait, metav1.ListOptions{LabelSelector: vitessComponentLabel + "=topology", Limit: 4, FieldSelector: activeDatabasePodFields})
				if listErr != nil {
					return listErr
				}
				if pods.Continue != "" || len(pods.Items) > 3 {
					return fmt.Errorf("Vitess topology rollout inventory exceeds its bound")
				}
				valid := true
				for _, pod := range pods.Items {
					if !c.vitessPodOwned(wait, pod, object.GetUID()) || pod.Labels[databaseOwner] != d.ID || pod.Labels[vitessComponentLabel] != "topology" || !vitessPodMatches(pod, d) || !databasePodPolicyMatches(pod, policy) {
						return fmt.Errorf("Vitess topology rollout ownership or runtime changed")
					}
					valid = valid && vitessPodReady(pod)
				}
				target, complete, stateErr := vitessTopologyRolloutTarget(pods.Items, expectedIdentity)
				if stateErr != nil {
					return stateErr
				}
				if complete {
					for _, pod := range pods.Items {
						if healthErr := health(wait, d, object, policy, pod); healthErr != nil {
							return healthErr
						}
					}
					return nil
				}
				if valid && target.Name != "" {
					// Require every voter, including a previous-identity voter, to
					// commit through its mounted TLS identity before disruption. The
					// replacement is checked here before the next voter is selected.
					for _, pod := range pods.Items {
						if healthErr := health(wait, d, object, policy, pod); healthErr != nil {
							return healthErr
						}
					}
					reviewedSpec, specFound, specErr := unstructured.NestedFieldCopy(object.Object, "spec")
					if specErr != nil || !specFound {
						return fmt.Errorf("Vitess topology root specification is invalid")
					}
					if err = before(); err != nil {
						return err
					}
					currentNamespace, namespaceErr := c.kube.CoreV1().Namespaces().Get(wait, ns, metav1.GetOptions{})
					currentRoot, rootErr := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns).Get(wait, "database", metav1.GetOptions{})
					currentLockserver, lockserverErr := c.dynamic.Resource(schema.GroupVersionResource{Group: "planetscale.com", Version: "v2", Resource: "etcdlockservers"}).Namespace(ns).Get(wait, etcdName, metav1.GetOptions{})
					currentPods, podsErr := c.kube.CoreV1().Pods(ns).List(wait, metav1.ListOptions{LabelSelector: vitessComponentLabel + "=topology", Limit: 4, FieldSelector: activeDatabasePodFields})
					if namespaceErr != nil || currentNamespace.UID != namespaceUID || currentNamespace.DeletionTimestamp != nil || currentNamespace.Labels[databaseOwner] != d.ID || currentNamespace.Labels[managedBy] != "hakopod" || rootErr != nil || currentRoot.GetUID() != object.GetUID() || currentRoot.GetDeletionTimestamp() != nil || currentRoot.GetLabels()[databaseOwner] != d.ID || currentRoot.GetLabels()[managedBy] != "hakopod" || lockserverErr != nil || currentLockserver.GetUID() != lockserver.GetUID() || currentLockserver.GetDeletionTimestamp() != nil || podsErr != nil || currentPods.Continue != "" || len(currentPods.Items) != len(pods.Items) {
						return fmt.Errorf("Vitess topology rollout ownership changed before deletion")
					}
					currentSpec, currentSpecFound, currentSpecErr := unstructured.NestedFieldCopy(currentRoot.Object, "spec")
					if currentSpecErr != nil || !currentSpecFound || !equality.Semantic.DeepEqual(currentSpec, reviewedSpec) {
						return fmt.Errorf("Vitess topology root specification changed before deletion")
					}
					currentIdentity, identityFound, identityErr := unstructured.NestedString(currentLockserver.Object, "spec", "annotations", vitessIdentityAnnotation)
					if identityErr != nil || !identityFound || currentIdentity != expectedIdentity || !c.vitessOwnedChain(wait, ns, currentLockserver.GetUID(), currentLockserver.GetOwnerReferences(), currentRoot.GetUID()) {
						return fmt.Errorf("Vitess topology rollout ownership changed before deletion")
					}
					priorPods := make(map[string]corev1.Pod, len(pods.Items))
					for _, pod := range pods.Items {
						priorPods[pod.Name] = pod
					}
					for _, currentPod := range currentPods.Items {
						prior, found := priorPods[currentPod.Name]
						if !found || currentPod.UID != prior.UID || currentPod.DeletionTimestamp != nil || currentPod.Annotations[vitessIdentityAnnotation] != prior.Annotations[vitessIdentityAnnotation] || !c.vitessPodOwned(wait, currentPod, currentRoot.GetUID()) || !vitessPodMatches(currentPod, d) || !databasePodPolicyMatches(currentPod, policy) || !vitessPodReady(currentPod) {
							return fmt.Errorf("Vitess topology rollout ownership changed before deletion")
						}
					}
					for _, currentPod := range currentPods.Items {
						if healthErr := health(wait, d, currentRoot, policy, currentPod); healthErr != nil {
							return healthErr
						}
					}
					if err = c.kube.CoreV1().Pods(ns).Delete(wait, target.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &target.UID}}); err != nil && !apierrors.IsNotFound(err) {
						return err
					}
				}
			}
		} else if !apierrors.IsNotFound(getErr) {
			return getErr
		}
		if err = sleepContext(wait, time.Second); err != nil {
			return fmt.Errorf("Vitess topology identity rollout did not converge: %w", err)
		}
	}
}

// Keep the namespace operator alive while Kubernetes removes its owned CR
// hierarchy. Deleting the namespace first would remove the reconciler too.
func (c *Client) deleteVitessController(ctx context.Context, d database.Resource, before func() error) (bool, error) {
	api := c.dynamic.Resource(vitessDatabaseResource).Namespace(DatabaseNamespace(d.ID))
	current, err := api.Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if current.GetUID() == "" || current.GetLabels()[databaseOwner] != d.ID || current.GetLabels()[managedBy] != "hakopod" {
		return false, fmt.Errorf("Vitess deletion controller ownership changed")
	}
	if current.GetDeletionTimestamp() != nil {
		return false, c.pauseVitessDeletionController(ctx, d, current.GetUID(), before)
	}
	if err = before(); err != nil {
		return false, err
	}
	uid := current.GetUID()
	foreground := metav1.DeletePropagationForeground
	err = api.Delete(ctx, current.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}, PropagationPolicy: &foreground})
	if err != nil {
		return false, err
	}
	return false, c.pauseVitessDeletionController(ctx, d, uid, before)
}

// Pause the namespace operator after root deletion is accepted so it cannot
// recreate children during garbage collection. The pinned operator has no
// native finalizers; Kubernetes removes the owned hierarchy. Backup credentials
// and revoked network access stay closed.
func (c *Client) pauseVitessDeletionController(ctx context.Context, d database.Resource, uid types.UID, before func() error) error {
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil {
		return err
	}
	if ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("Vitess deletion namespace ownership changed")
	}
	current, err := c.dynamic.Resource(vitessDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.GetUID() != uid || current.GetDeletionTimestamp() == nil {
		return fmt.Errorf("Vitess deletion was not accepted for the owned controller")
	}
	operator, err := c.kube.AppsV1().Deployments(ns.Name).Get(ctx, "vitess-operator", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !vitessNamespaceObjectOwned(operator, d, ns.UID) {
		return fmt.Errorf("Vitess deletion operator ownership changed")
	}
	if operator.Spec.Replicas != nil && *operator.Spec.Replicas == 0 {
		return nil
	}
	operator.Spec.Replicas = ptr(int32(0))
	if err = before(); err != nil {
		return err
	}
	_, err = c.kube.AppsV1().Deployments(ns.Name).Update(ctx, operator, metav1.UpdateOptions{})
	return err
}
