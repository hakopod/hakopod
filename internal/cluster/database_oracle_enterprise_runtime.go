package cluster

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func (c *Client) applyOracleEnterpriseDatabase(ctx context.Context, d database.Resource, password []byte, before func() error) error {
	if err := d.Spec.Validate(); err != nil {
		return err
	}
	if !oracleEnterprise(d.Spec) {
		return fmt.Errorf("invalid Oracle Enterprise deployment")
	}
	if err := c.oracleEnterpriseControllerAvailable(ctx); err != nil {
		return err
	}
	p, err := c.databasePolicy(ctx, d)
	if err != nil {
		return err
	}
	if err = c.validateDatabasePlacementNodes(ctx, d.Spec, p); err != nil {
		return err
	}
	nodes := d.Spec.Placement.NodeNames
	if len(nodes) == 0 && p != nil {
		nodes = p.nodes()
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
		return fmt.Errorf("Oracle Enterprise namespace could not be reconciled")
	}
	if ns, err = c.oracleEnterpriseNamespace(ctx, d); err != nil {
		return err
	}
	if len(password) != 64 {
		return fmt.Errorf("Oracle application credential has invalid length")
	}
	if _, err = hex.DecodeString(string(password)); err != nil {
		return fmt.Errorf("Oracle application credential has invalid encoding")
	}
	secrets := c.kube.CoreV1().Secrets(ns.Name)
	credential, err := secrets.Get(ctx, "database-credentials", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		credential = &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-credentials"), Type: corev1.SecretTypeBasicAuth, Immutable: ptr(true), Data: map[string][]byte{"username": []byte("app"), "password": password}}
		if err = before(); err != nil {
			return err
		}
		credential, err = secrets.Create(ctx, credential, metav1.CreateOptions{})
	}
	if err != nil {
		return fmt.Errorf("Oracle application credentials could not be reconciled")
	}
	if err = databaseIdentityOwned(credential, d, ns.UID); err != nil || credential.Immutable == nil || !*credential.Immutable || subtle.ConstantTimeCompare(credential.Data["password"], password) != 1 || string(credential.Data["username"]) != "app" {
		return fmt.Errorf("Oracle application credential identity changed")
	}
	if err = c.databaseNetworkPolicy(ctx, d, before); err != nil {
		return err
	}
	if err = c.prepareDatabaseIdentity(ctx, d, before); err != nil {
		return err
	}
	if err = c.prepareOracleEnterpriseAccess(ctx, d, before); err != nil {
		return err
	}
	if err = c.prepareOracleEnterpriseBootstrap(ctx, d, before); err != nil {
		return err
	}
	registry, err := c.prepareOracleEnterpriseRegistry(ctx, d, before)
	if err != nil {
		return err
	}
	root := oracleEnterpriseObject(d, 0, registry, p, nodes)
	root.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: ns.Name, UID: ns.UID}})
	root, err = c.reconcileOracleEnterpriseObject(ctx, d, root, oracleEnterpriseResource, before)
	if err != nil {
		return err
	}
	owner := []metav1.OwnerReference{{APIVersion: "database.oracle.com/v4", Kind: "SingleInstanceDatabase", Name: root.GetName(), UID: root.GetUID(), Controller: ptr(true), BlockOwnerDeletion: ptr(true)}}
	for i := 1; i < d.Spec.Members(); i++ {
		member := oracleEnterpriseObject(d, i, registry, p, nodes)
		member.SetOwnerReferences(owner)
		if _, err = c.reconcileOracleEnterpriseObject(ctx, d, member, oracleEnterpriseResource, before); err != nil {
			return err
		}
	}
	if d.Spec.Mode == "cluster" {
		broker := oracleEnterpriseBroker(d, registry, p, nodes)
		broker.SetOwnerReferences(owner)
		if _, err = c.reconcileOracleEnterpriseObject(ctx, d, broker, oracleEnterpriseBrokerResource, before); err != nil {
			return err
		}
	}
	// Publish no members until maintenance has checked native roles and TCPS.
	return c.reconcileOracleEnterpriseRoute(ctx, d, nil, before)
}

func (c *Client) reconcileOracleEnterpriseObject(ctx context.Context, d database.Resource, desired *unstructured.Unstructured, gvr schema.GroupVersionResource, before func() error) (*unstructured.Unstructured, error) {
	api := c.dynamic.Resource(gvr).Namespace(DatabaseNamespace(d.ID))
	current, err := api.Get(ctx, desired.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return nil, err
		}
		return api.Create(ctx, desired, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("Oracle controller resource is unavailable")
	}
	if current.GetDeletionTimestamp() != nil || current.GetLabels()[databaseOwner] != d.ID || current.GetLabels()[managedBy] != "hakopod" || current.GetLabels()["hakopod.io/project"] != d.Project || current.GetLabels()["hakopod.io/environment"] != d.Environment || !reflect.DeepEqual(current.GetOwnerReferences(), desired.GetOwnerReferences()) {
		return nil, fmt.Errorf("Oracle controller resource ownership changed")
	}
	// Never overwrite broker operation tokens or observed role changes while
	// reconciling the accepted deployment. No resize is accepted for Oracle.
	if current.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return nil, fmt.Errorf("Oracle deployment revision changed; provision a separate reviewed target")
	}
	beforeSpec := current.DeepCopy()
	wantedSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")
	currentSpec, _, _ := unstructured.NestedMap(current.Object, "spec")
	overlayDatabaseFields(currentSpec, wantedSpec)
	if err = unstructured.SetNestedMap(current.Object, currentSpec, "spec"); err != nil {
		return nil, err
	}
	annotations := current.GetAnnotations()
	annotations[oracleEnterprisePodPolicy] = desired.GetAnnotations()[oracleEnterprisePodPolicy]
	current.SetAnnotations(annotations)
	if reflect.DeepEqual(beforeSpec.Object, current.Object) {
		return current, nil
	}
	if err = before(); err != nil {
		return nil, err
	}
	return api.Update(ctx, current, metav1.UpdateOptions{})
}

func (c *Client) prepareOracleEnterpriseAccess(ctx context.Context, d database.Resource, before func() error) error {
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		return err
	}
	api := c.kube.CoreV1().Secrets(ns.Name)
	access, err := api.Get(ctx, "database-oracle-access", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		data := map[string][]byte{}
		for _, key := range []string{"admin", "wallet"} {
			value := make([]byte, 32)
			if _, err = rand.Read(value); err != nil {
				return err
			}
			data[key] = []byte(hex.EncodeToString(value))
		}
		access = &corev1.Secret{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-oracle-access"), Immutable: ptr(true), Data: data}
		if err = before(); err != nil {
			return err
		}
		access, err = api.Create(ctx, access, metav1.CreateOptions{})
	}
	if err != nil {
		return fmt.Errorf("Oracle administrator credentials are unavailable")
	}
	if err = databaseIdentityOwned(access, d, ns.UID); err != nil || access.Immutable == nil || !*access.Immutable {
		return fmt.Errorf("Oracle administrator credential identity changed")
	}
	for _, key := range []string{"admin", "wallet"} {
		if len(access.Data[key]) != 64 {
			return fmt.Errorf("Oracle administrator credential is invalid")
		}
		if _, err = hex.DecodeString(string(access.Data[key])); err != nil {
			return fmt.Errorf("Oracle administrator credential is invalid")
		}
	}
	return nil
}

func (c *Client) oracleEnterpriseExecTarget(ctx context.Context, d database.Resource, member database.Member) (*corev1.Pod, string, error) {
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		return nil, "", err
	}
	pod, err := c.kube.CoreV1().Pods(ns.Name).Get(ctx, member.Name, metav1.GetOptions{})
	if err != nil || pod.DeletionTimestamp != nil || pod.UID != types.UID(member.UID) || pod.UID == "" || pod.Labels[databaseOwner] != d.ID || pod.Labels[managedBy] != "hakopod" || pod.Labels[oracleEnterpriseMemberLabel] != "true" {
		return nil, "", fmt.Errorf("Oracle member identity changed")
	}
	controller := metav1.GetControllerOf(pod)
	if controller == nil || controller.Kind != "SingleInstanceDatabase" || controller.APIVersion != "database.oracle.com/v4" || !oracleEnterpriseMemberAllowed(d, controller.Name) {
		return nil, "", fmt.Errorf("Oracle member controller ownership changed")
	}
	object, err := c.dynamic.Resource(oracleEnterpriseResource).Namespace(ns.Name).Get(ctx, controller.Name, metav1.GetOptions{})
	if err != nil || object.GetUID() != controller.UID || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" || object.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) || object.GetDeletionTimestamp() != nil {
		return nil, "", fmt.Errorf("Oracle member controller identity changed")
	}
	parent := metav1.GetControllerOf(object)
	if controller.Name == "database" {
		owned := false
		for _, owner := range object.GetOwnerReferences() {
			owned = owned || owner.Kind == "Namespace" && owner.Name == ns.Name && owner.UID == ns.UID
		}
		if !owned {
			return nil, "", fmt.Errorf("Oracle root namespace ownership changed")
		}
	} else {
		root, rootErr := c.dynamic.Resource(oracleEnterpriseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
		if rootErr != nil || parent == nil || parent.Kind != "SingleInstanceDatabase" || parent.Name != "database" || parent.UID != root.GetUID() || root.GetLabels()[databaseOwner] != d.ID || root.GetDeletionTimestamp() != nil {
			return nil, "", fmt.Errorf("Oracle standby root ownership changed")
		}
	}
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != d.Spec.Oracle.Image || pod.Spec.Containers[0].Name != controller.Name {
		return nil, "", fmt.Errorf("Oracle member does not use its accepted image")
	}
	return pod, controller.Name, nil
}

func oracleEnterpriseMemberAllowed(d database.Resource, name string) bool {
	for i := 0; i < d.Spec.Members(); i++ {
		if oracleEnterpriseMemberName(i) == name {
			return true
		}
	}
	return false
}

func (c *Client) oracleEnterpriseObjectOwned(ctx context.Context, d database.Resource, object *unstructured.Unstructured) error {
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		return err
	}
	if object == nil || object.GetNamespace() != ns.Name || object.GetUID() == "" || object.GetDeletionTimestamp() != nil || object.GetLabels()[databaseOwner] != d.ID || object.GetLabels()[managedBy] != "hakopod" || object.GetLabels()["hakopod.io/project"] != d.Project || object.GetLabels()["hakopod.io/environment"] != d.Environment || object.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return fmt.Errorf("Oracle controller scope or revision changed")
	}
	if object.GetKind() == "SingleInstanceDatabase" && object.GetName() == "database" {
		owners := object.GetOwnerReferences()
		if len(owners) != 1 || owners[0].APIVersion != "v1" || owners[0].Kind != "Namespace" || owners[0].Name != ns.Name || owners[0].UID != ns.UID {
			return fmt.Errorf("Oracle root ownership changed")
		}
		return nil
	}
	if !(object.GetKind() == "SingleInstanceDatabase" && oracleEnterpriseMemberAllowed(d, object.GetName()) || object.GetKind() == "DataguardBroker" && object.GetName() == "database-broker" && d.Spec.Mode == "cluster") {
		return fmt.Errorf("Oracle controller is outside the accepted topology")
	}
	root, err := c.dynamic.Resource(oracleEnterpriseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("Oracle root controller is unavailable")
	}
	if err = c.oracleEnterpriseObjectOwned(ctx, d, root); err != nil {
		return err
	}
	owner := metav1.GetControllerOf(object)
	if len(object.GetOwnerReferences()) != 1 || owner == nil || owner.APIVersion != "database.oracle.com/v4" || owner.Kind != "SingleInstanceDatabase" || owner.Name != "database" || owner.UID != root.GetUID() {
		return fmt.Errorf("Oracle controller root ownership changed")
	}
	return nil
}

func (c *Client) reconcileOracleEnterpriseRoute(ctx context.Context, d database.Resource, primary *database.Member, before func() error) error {
	ns, err := c.oracleEnterpriseNamespace(ctx, d)
	if err != nil {
		return err
	}
	selector := map[string]string{databaseOwner: d.ID, "hakopod.io/oracle-pod": "unverified"}
	if primary != nil {
		if primary.Role != "primary" || !primary.Ready {
			return fmt.Errorf("Oracle endpoint requires a verified primary")
		}
		if _, _, err = c.oracleEnterpriseExecTarget(ctx, d, *primary); err != nil {
			return err
		}
		selector["hakopod.io/oracle-pod"] = primary.Name
	}
	desired := &corev1.Service{ObjectMeta: databaseIdentityMeta(d, ns.UID, "database-rw"), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: selector, Ports: []corev1.ServicePort{{Name: "tcps", Port: 2484, TargetPort: intstr.FromInt(2484), Protocol: corev1.ProtocolTCP}}}}
	api := c.kube.CoreV1().Services(ns.Name)
	current, err := api.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if err = before(); err != nil {
			return err
		}
		_, err = api.Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return fmt.Errorf("Oracle write endpoint is unavailable")
	}
	if !mongodbSupportOwned(current, d, ns.UID) || current.Spec.Type != corev1.ServiceTypeClusterIP || len(current.Spec.ExternalIPs) != 0 || current.Spec.ExternalName != "" || !reflect.DeepEqual(current.Spec.Ports, desired.Spec.Ports) {
		return fmt.Errorf("Oracle write endpoint ownership or protocol changed")
	}
	if primary != nil && current.Annotations[oracleRoleRequest] != "" {
		return fmt.Errorf("Oracle write endpoint is closed for a reviewed role operation")
	}
	if reflect.DeepEqual(current.Spec.Selector, selector) {
		return nil
	}
	current.Spec.Selector = selector
	if err = before(); err != nil {
		return err
	}
	_, err = api.Update(ctx, current, metav1.UpdateOptions{})
	return err
}

func (c *Client) oracleEnterpriseRevisionApplied(ctx context.Context, d database.Resource) (bool, error) {
	if _, err := c.oracleEnterpriseNamespace(ctx, d); err != nil {
		return false, err
	}
	p, err := c.databasePolicy(ctx, d)
	if err != nil {
		return false, err
	}
	nodes := d.Spec.Placement.NodeNames
	if len(nodes) == 0 && p != nil {
		nodes = p.nodes()
	}
	registry := ""
	if d.Spec.Oracle.RegistryCredential != "" {
		registry = "hp-registry-" + RegistryScope(d.Project, d.Environment, d.Spec.Oracle.RegistryCredential)[:20]
	}
	type check struct {
		object   *unstructured.Unstructured
		resource schema.GroupVersionResource
	}
	checks := []check{}
	for i := 0; i < d.Spec.Members(); i++ {
		checks = append(checks, check{oracleEnterpriseObject(d, i, registry, p, nodes), oracleEnterpriseResource})
	}
	if d.Spec.Mode == "cluster" {
		checks = append(checks, check{oracleEnterpriseBroker(d, registry, p, nodes), oracleEnterpriseBrokerResource})
	}
	for _, wanted := range checks {
		current, err := c.dynamic.Resource(wanted.resource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, wanted.object.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if current.GetLabels()[databaseOwner] != d.ID || current.GetLabels()[managedBy] != "hakopod" || current.GetDeletionTimestamp() != nil {
			return false, fmt.Errorf("Oracle revision ownership changed")
		}
		if current.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) || current.GetAnnotations()[oracleEnterprisePodPolicy] != wanted.object.GetAnnotations()[oracleEnterprisePodPolicy] {
			return false, nil
		}
		if err = c.oracleEnterpriseObjectOwned(ctx, d, current); err != nil {
			return false, err
		}
		spec, _, _ := unstructured.NestedMap(current.Object, "spec")
		desired, _, _ := unstructured.NestedMap(wanted.object.Object, "spec")
		copy := current.DeepCopy()
		overlayDatabaseFields(spec, desired)
		if !reflect.DeepEqual(copy.Object["spec"], spec) {
			return false, nil
		}
	}
	return true, nil
}
