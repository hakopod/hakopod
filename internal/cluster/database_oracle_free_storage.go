package cluster

import (
	"context"
	"fmt"
	"reflect"
	"strconv"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Persist a concrete class in SIDB because upstream additional PVC creation
// requires one. A later default-class change cannot move an existing database.
func (c *Client) oracleFreeStorageClass(ctx context.Context, d database.Resource, policy *DatabasePolicy) (string, error) {
	if policy != nil {
		return policy.StorageClass, nil
	}
	object, err := c.dynamic.Resource(oracleDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err == nil {
		if err = c.oracleEnterpriseObjectOwned(ctx, d, object); err != nil {
			return "", err
		}
		name, _, _ := unstructured.NestedString(object.Object, "spec", "persistence", "oradata", "storageClass")
		if name == "" {
			return "", fmt.Errorf("Oracle Free storage class identity is missing")
		}
		if _, err = c.kube.StorageV1().StorageClasses().Get(ctx, name, metav1.GetOptions{}); err != nil {
			return "", fmt.Errorf("Oracle Free storage class is unavailable")
		}
		return name, nil
	}
	if !apierrors.IsNotFound(err) {
		return "", err
	}
	classes, err := c.kube.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{Limit: 33})
	if err != nil || classes.Continue != "" || len(classes.Items) > 32 {
		return "", fmt.Errorf("Oracle Free storage class inventory is unavailable or exceeds its bound")
	}
	selected := ""
	for _, class := range classes.Items {
		if class.DeletionTimestamp != nil || (class.Annotations["storageclass.kubernetes.io/is-default-class"] != "true" && class.Annotations["storageclass.beta.kubernetes.io/is-default-class"] != "true") {
			continue
		}
		if selected != "" {
			return "", fmt.Errorf("Oracle Free requires one unambiguous default storage class")
		}
		selected = class.Name
	}
	if selected == "" {
		return "", fmt.Errorf("Oracle Free requires a default or allocated storage class")
	}
	return selected, nil
}

func (c *Client) oracleFreeClaimsOwned(ctx context.Context, d database.Resource, object *unstructured.Unstructured) error {
	storage, _, _ := unstructured.NestedString(object.Object, "spec", "persistence", "oradata", "storageClass")
	size := resource.MustParse(fmt.Sprintf("%dGi", d.Spec.StorageGiB))
	for _, name := range []string{"database", "database-additional-0"} {
		claim, err := c.kube.CoreV1().PersistentVolumeClaims(DatabaseNamespace(d.ID)).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		owner := metav1.GetControllerOf(claim)
		if claim.UID == "" || claim.DeletionTimestamp != nil || len(claim.OwnerReferences) != 1 || owner == nil || owner.APIVersion != "database.oracle.com/v4" || owner.Kind != "SingleInstanceDatabase" || owner.Name != "database" || owner.UID != object.GetUID() {
			return fmt.Errorf("Oracle Free storage ownership changed")
		}
		request := claim.Spec.Resources.Requests[corev1.ResourceStorage]
		if storage == "" || claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName != storage || len(claim.Spec.AccessModes) != 1 || claim.Spec.AccessModes[0] != corev1.ReadWriteOnce || request.Cmp(size) != 0 || claim.Spec.VolumeMode != nil && *claim.Spec.VolumeMode != corev1.PersistentVolumeFilesystem || claim.Spec.DataSource != nil || claim.Spec.DataSourceRef != nil || claim.Spec.Selector != nil || claim.Spec.VolumeName == "" || claim.Status.Phase != corev1.ClaimBound {
			return fmt.Errorf("Oracle Free storage specification or binding changed")
		}
		volume, err := c.kube.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		if err != nil || volume.UID == "" || volume.DeletionTimestamp != nil || volume.Spec.ClaimRef == nil || volume.Spec.ClaimRef.Namespace != claim.Namespace || volume.Spec.ClaimRef.Name != claim.Name || volume.Spec.ClaimRef.UID != claim.UID {
			return fmt.Errorf("Oracle Free backing volume identity changed")
		}
	}
	return nil
}

func (c *Client) oracleFreeRevisionApplied(ctx context.Context, d database.Resource) (bool, error) {
	current, err := c.dynamic.Resource(oracleDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current.GetAnnotations()["hakopod.io/database-revision"] != strconv.FormatInt(d.Revision, 10) {
		return false, nil
	}
	if err = c.oracleEnterpriseObjectOwned(ctx, d, current); err != nil {
		return false, err
	}
	desired, err := c.databaseObject(ctx, d)
	if err != nil {
		return false, err
	}
	return oracleFreeSpecMatches(current, desired), nil
}

// Reject legacy or foreign objects before a scoped controller is started.
func (c *Client) oracleFreeResourcesPreflight(ctx context.Context, d database.Resource) error {
	root, err := c.dynamic.Resource(oracleDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if err = c.oracleEnterpriseObjectOwned(ctx, d, root); err != nil {
			return err
		}
		desired, err := c.databaseObject(ctx, d)
		if err != nil {
			return err
		}
		if !oracleFreeSpecMatches(root, desired) {
			return fmt.Errorf("Oracle Free existing resource policy changed")
		}
	}
	claims, err := c.kube.CoreV1().PersistentVolumeClaims(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{Limit: 3})
	if err != nil || claims.Continue != "" || len(claims.Items) > 2 {
		return fmt.Errorf("Oracle Free claim inventory exceeds its bound")
	}
	for _, claim := range claims.Items {
		owner := metav1.GetControllerOf(&claim)
		if root == nil || (claim.Name != "database" && claim.Name != "database-additional-0") || len(claim.OwnerReferences) != 1 || owner == nil || owner.APIVersion != "database.oracle.com/v4" || owner.Kind != "SingleInstanceDatabase" || owner.Name != "database" || owner.UID != root.GetUID() {
			return fmt.Errorf("Oracle Free will not adopt an existing data claim")
		}
	}
	services, err := c.kube.CoreV1().Services(DatabaseNamespace(d.ID)).List(ctx, metav1.ListOptions{Limit: 2})
	if err != nil || services.Continue != "" || len(services.Items) > 1 {
		return fmt.Errorf("Oracle Free service inventory exceeds its bound")
	}
	for _, service := range services.Items {
		owner := metav1.GetControllerOf(&service)
		if root == nil || service.Name != "database" || len(service.OwnerReferences) != 1 || owner == nil || owner.APIVersion != "database.oracle.com/v4" || owner.Kind != "SingleInstanceDatabase" || owner.Name != "database" || owner.UID != root.GetUID() {
			return fmt.Errorf("Oracle Free will not adopt an existing service")
		}
	}
	return nil
}

func (c *Client) oracleFreeServiceOwned(ctx context.Context, d database.Resource, object *unstructured.Unstructured) error {
	service, err := c.kube.CoreV1().Services(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
	if err != nil {
		return err
	}
	owner := metav1.GetControllerOf(service)
	if service.UID == "" || service.DeletionTimestamp != nil || len(service.OwnerReferences) != 1 || owner == nil || owner.APIVersion != "database.oracle.com/v4" || owner.Kind != "SingleInstanceDatabase" || owner.Name != "database" || owner.UID != object.GetUID() {
		return fmt.Errorf("Oracle Free service ownership changed")
	}
	expected := map[string]string{"app": "database", databaseOwner: d.ID, oracleEnterpriseMemberLabel: "true"}
	if !reflect.DeepEqual(service.Spec.Selector, expected) || service.Spec.Type != corev1.ServiceTypeClusterIP || service.Spec.ClusterIP == "" || service.Spec.ClusterIP == "None" || service.Spec.ExternalName != "" || len(service.Spec.ExternalIPs) != 0 || service.Spec.PublishNotReadyAddresses || len(service.Spec.Ports) != 1 {
		return fmt.Errorf("Oracle Free service route changed")
	}
	port := service.Spec.Ports[0]
	if port.Name != "tcps" || port.Protocol != corev1.ProtocolTCP || port.Port != 2484 || port.TargetPort.IntVal != 2484 || port.TargetPort.Type != 0 || port.NodePort != 0 {
		return fmt.Errorf("Oracle Free requires a private TCPS service")
	}
	return nil
}
