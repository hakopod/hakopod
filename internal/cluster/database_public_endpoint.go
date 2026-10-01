package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

const databasePublicEndpointLabel = "hakopod.io/database-public-endpoint-id"
const databasePublicEndpointClaimLabel = "hakopod.io/database-public-endpoint-claim"

const (
	databasePublicEndpointClaimStateKey    = "route_state"
	databasePublicEndpointClaimBaselineKey = "reload_baseline"
	databasePublicEndpointClaimRevisionKey = "closure_revision"
	databasePublicEndpointClaimAckKey      = "closure_acknowledged"
	databasePublicEndpointNeverPublished   = "never_published"
	databasePublicEndpointPublished        = "published"
	databasePublicEndpointClosed           = "closed"
)

func databasePublicEndpointName(endpointID string) string {
	return "database-public-" + endpointID[:min(16, len(endpointID))]
}

func databasePublicEndpointFrontend(endpointID string) string {
	return "hp-db-" + ownerID(endpointID)
}

func databasePublicEndpointPrefix(d database.Resource, endpoint database.PublicEndpoint) string {
	return "tcpcr_" + DatabaseNamespace(d.ID) + "_" + databasePublicEndpointFrontend(endpoint.ID)
}

func databasePublicEndpointBackend(d database.Resource, purpose string) (database.PublicEndpointRoute, error) {
	return database.PublicEndpointRouteFor(d.Spec, purpose)
}

func databasePublicEndpointModels(d database.Resource, endpoint database.PublicEndpoint) ([]any, error) {
	backend, err := databasePublicEndpointBackend(d, endpoint.Spec.Purpose)
	if err != nil {
		return nil, err
	}
	allocations, err := database.PublicEndpointAllocations(endpoint)
	if err != nil {
		return nil, err
	}
	models := make([]any, 0, len(allocations))
	for index, member := range allocations {
		frontend, service := databasePublicEndpointFrontend(endpoint.ID), backend.BackendService
		if len(endpoint.MemberAllocations) > 0 {
			frontend += "-" + strconv.Itoa(index)
			if d.Spec.Engine == "mongodb" {
				service = mongodbPublicEndpointServiceName(endpoint, member)
			}
			if d.Spec.Engine == "redis" {
				service = redisPublicEndpointServiceName(endpoint, member)
			}
		}
		models = append(models, map[string]any{
			"name":    frontend,
			"service": map[string]any{"name": service, "port": int64(backend.BackendPort)},
			"frontend": map[string]any{
				"name":                  frontend,
				"binds":                 map[string]any{"v4": map[string]any{"name": "v4", "address": "0.0.0.0", "port": int64(member.Allocation.Port)}},
				"acl_list":              []any{map[string]any{"acl_name": "allowed_source", "criterion": "src", "value": strings.Join(endpoint.Spec.SourceCIDRs, " ")}},
				"tcp_request_rule_list": []any{map[string]any{"type": "connection", "action": "reject", "cond": "unless", "cond_test": "allowed_source"}},
				"client_timeout":        int64(300000), "maxconn": int64(endpoint.Spec.MaxConnections),
			},
		})
	}
	return models, nil
}

func databasePublicEndpointObject(d database.Resource, endpoint database.PublicEndpoint, namespaceUID string, ingressClass string) (*unstructured.Unstructured, error) {
	models, err := databasePublicEndpointModels(d, endpoint)
	if err != nil {
		return nil, err
	}
	labels := databaseLabels(d)
	labels[databasePublicEndpointLabel] = endpoint.ID
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "ingress.v3.haproxy.org/v3", "kind": "TCP", "spec": models}}
	object.SetName(databasePublicEndpointName(endpoint.ID))
	object.SetNamespace(DatabaseNamespace(d.ID))
	object.SetLabels(labels)
	object.SetAnnotations(map[string]string{"ingress.class": ingressClass, "hakopod.io/database-endpoint-revision": strconv.FormatInt(endpoint.Revision, 10)})
	object.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Namespace", Name: DatabaseNamespace(d.ID), UID: typesUID(namespaceUID)}})
	return object, nil
}

// typesUID is kept local so database endpoint ownership cannot accidentally be
// expressed as application ownership.
func typesUID(value string) types.UID { return types.UID(value) }

func databasePublicEndpointOwned(object *unstructured.Unstructured, d database.Resource, endpoint database.PublicEndpoint, namespaceUID string) bool {
	if object == nil || object.GetDeletionTimestamp() != nil || object.GetNamespace() != DatabaseNamespace(d.ID) || object.GetName() != databasePublicEndpointName(endpoint.ID) {
		return false
	}
	labels := object.GetLabels()
	if labels[managedBy] != "hakopod" || labels[databaseOwner] != d.ID || labels[databasePublicEndpointLabel] != endpoint.ID {
		return false
	}
	for _, ref := range object.GetOwnerReferences() {
		if ref.APIVersion == "v1" && ref.Kind == "Namespace" && ref.Name == object.GetNamespace() && string(ref.UID) == namespaceUID {
			return true
		}
	}
	return false
}

func (c *Client) validateDatabasePublicEndpointAllocation(endpoint database.PublicEndpoint) error {
	wanted, err := database.PublicEndpointAllocations(endpoint)
	if err != nil {
		return err
	}
	configured := c.DatabasePublicEndpointAllocations()
	for _, member := range wanted {
		if !slices.Contains(configured, member.Allocation) {
			return fmt.Errorf("database public endpoint allocation is outside the configured operator inventory")
		}
	}
	return nil
}

// ValidateDatabasePublicEndpoint is read-only. It checks trusted allocation,
// DNS, the owned ingress controller, the shared port claim and every supported
// HAProxy TCP resource before a route can be applied.
func (c *Client) ValidateDatabasePublicEndpoint(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) error {
	if err := database.PublicEndpointAvailability(d.Spec); err != nil {
		return err
	}
	if c.options.DeploymentMode == DeploymentManagedCloud && !c.options.ManagedDatabasePublicEndpointsQualified {
		return fmt.Errorf("managed Cloud database public endpoints have not passed release qualification")
	}
	if _, err := databasePublicEndpointBackend(d, endpoint.Spec.Purpose); err != nil {
		return err
	}
	if err := ValidateDatabasePublicEndpointOptions(c.options); err != nil {
		return err
	}
	if err := c.validateDatabasePublicEndpointAllocation(endpoint); err != nil {
		return err
	}
	allocations, err := database.PublicEndpointAllocations(endpoint)
	if err != nil {
		return err
	}
	for _, member := range allocations {
		if err := c.VerifyDatabasePublicEndpointDNS(ctx, member.Allocation); err != nil {
			return err
		}
	}
	if c.dynamic == nil || c.kube == nil {
		return fmt.Errorf("database public endpoints require Kubernetes and the HAProxy TCP API")
	}
	controller, err := c.publicTCPController(ctx)
	if err != nil {
		return err
	}
	for _, member := range allocations {
		if !controller[member.Allocation.Port] {
			return fmt.Errorf("database public endpoint port is not provisioned on the owned ingress controller")
		}
	}
	for _, member := range allocations {
		claim, claimErr := c.kube.CoreV1().ConfigMaps(c.options.ProxyNamespace).Get(ctx, publicTCPClaimName(member.Allocation.Port), metav1.GetOptions{})
		if claimErr == nil && (claim.Labels[databasePublicEndpointClaimLabel] != "true" || claim.Labels[databaseOwner] != d.ID || claim.Labels[databasePublicEndpointLabel] != endpoint.ID) {
			return fmt.Errorf("database public endpoint port is reserved by another owner")
		}
		if claimErr != nil && !apierrors.IsNotFound(claimErr) {
			return claimErr
		}
	}
	for _, resource := range []schema.GroupVersionResource{publicTCPResource, {Group: "ingress.v1.haproxy.org", Version: "v1", Resource: "tcps"}} {
		list, listErr := c.dynamic.Resource(resource).List(ctx, metav1.ListOptions{Limit: 256})
		if apierrors.IsNotFound(listErr) && resource != publicTCPResource {
			continue
		}
		if listErr != nil {
			return fmt.Errorf("inspect HAProxy TCP routes: %w", listErr)
		}
		if list.GetContinue() != "" || len(list.Items) > 256 {
			return fmt.Errorf("public TCP route inventory exceeds the 256-resource review limit")
		}
		for i := range list.Items {
			item := &list.Items[i]
			if item.GetAnnotations()["ingress.class"] != "" && item.GetAnnotations()["ingress.class"] != c.options.IngressClass {
				continue
			}
			if resource == publicTCPResource && item.GetNamespace() == DatabaseNamespace(d.ID) && item.GetName() == databasePublicEndpointName(endpoint.ID) {
				ns, e := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
				if e != nil || !databasePublicEndpointOwned(item, d, endpoint, string(ns.UID)) {
					return fmt.Errorf("database public endpoint route ownership changed")
				}
				continue
			}
			models, ok, e := unstructured.NestedSlice(item.Object, "spec")
			if e != nil || !ok {
				return fmt.Errorf("cannot inspect HAProxy TCP route %s/%s", item.GetNamespace(), item.GetName())
			}
			for _, raw := range models {
				model, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("invalid existing HAProxy TCP model")
				}
				binds, _, _ := unstructured.NestedMap(model, "frontend", "binds")
				values := []any{}
				for _, bind := range binds {
					values = append(values, bind)
				}
				if old, found, _ := unstructured.NestedSlice(model, "frontend", "binds"); found {
					values = append(values, old...)
				}
				for _, rawBind := range values {
					bind, ok := rawBind.(map[string]any)
					if !ok {
						return fmt.Errorf("invalid existing HAProxy TCP bind")
					}
					port, found, _ := unstructured.NestedInt64(bind, "port")
					if !found {
						return fmt.Errorf("existing HAProxy TCP port ranges cannot be reviewed automatically")
					}
					conflict := false
					for _, member := range allocations {
						conflict = conflict || int32(port) == member.Allocation.Port
					}
					if conflict {
						return fmt.Errorf("database public endpoint port conflicts with HAProxy route %s/%s", item.GetNamespace(), item.GetName())
					}
				}
			}
		}
	}
	return nil
}

func (c *Client) reserveDatabasePublicEndpoint(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, before func() error) error {
	allocations, err := database.PublicEndpointAllocations(endpoint)
	if err != nil {
		return err
	}
	labels := databaseLabels(d)
	labels[databasePublicEndpointClaimLabel] = "true"
	labels[databasePublicEndpointLabel] = endpoint.ID
	for _, member := range allocations {
		current := endpoint
		current.Allocation = member.Allocation
		claim := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: publicTCPClaimName(member.Allocation.Port), Namespace: c.options.ProxyNamespace, Labels: labels}, Data: map[string]string{"database_id": d.ID, "endpoint_id": endpoint.ID, "allocation_id": member.Allocation.ID, databasePublicEndpointClaimStateKey: databasePublicEndpointNeverPublished}}
		if err = before(); err != nil {
			return err
		}
		created, createErr := c.kube.CoreV1().ConfigMaps(claim.Namespace).Create(ctx, claim, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(createErr) {
			created, createErr = c.kube.CoreV1().ConfigMaps(claim.Namespace).Get(ctx, claim.Name, metav1.GetOptions{})
		}
		if createErr != nil {
			return createErr
		}
		if err = validateDatabasePublicEndpointClaim(created, d, current); err != nil {
			return fmt.Errorf("database public endpoint port was concurrently reserved")
		}
	}
	return nil
}

func validateDatabasePublicEndpointClaim(claim *corev1.ConfigMap, d database.Resource, endpoint database.PublicEndpoint) error {
	if claim == nil || claim.DeletionTimestamp != nil || claim.Labels[databasePublicEndpointClaimLabel] != "true" || claim.Labels[databaseOwner] != d.ID || claim.Labels[databasePublicEndpointLabel] != endpoint.ID || claim.Data["database_id"] != d.ID || claim.Data["endpoint_id"] != endpoint.ID || claim.Data["allocation_id"] != endpoint.Allocation.ID {
		return fmt.Errorf("database public endpoint claim ownership changed")
	}
	return nil
}

func databasePublicEndpointClaimBaseline(claim *corev1.ConfigMap, endpoint database.PublicEndpoint) (map[string]string, bool, error) {
	raw := claim.Data[databasePublicEndpointClaimBaselineKey]
	if raw == "" {
		return nil, false, nil
	}
	if claim.Data[databasePublicEndpointClaimRevisionKey] != strconv.FormatInt(endpoint.Revision, 10) || len(raw) > 4096 {
		return nil, false, fmt.Errorf("database public endpoint closure baseline does not match this revision")
	}
	var baseline map[string]string
	if err := json.Unmarshal([]byte(raw), &baseline); err != nil || len(baseline) == 0 || len(baseline) > publicTCPMaxIngressPods {
		return nil, false, fmt.Errorf("database public endpoint closure baseline is invalid")
	}
	for uid, pid := range baseline {
		if uid == "" {
			return nil, false, fmt.Errorf("database public endpoint closure baseline is invalid")
		}
		if _, err := strconv.ParseUint(pid, 10, 64); err != nil {
			return nil, false, fmt.Errorf("database public endpoint closure baseline is invalid")
		}
	}
	return baseline, true, nil
}

func (c *Client) databasePublicEndpointClaim(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) (*corev1.ConfigMap, error) {
	claim, err := c.kube.CoreV1().ConfigMaps(c.options.ProxyNamespace).Get(ctx, publicTCPClaimName(endpoint.Allocation.Port), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err = validateDatabasePublicEndpointClaim(claim, d, endpoint); err != nil {
		return nil, err
	}
	return claim, nil
}

func (c *Client) persistDatabasePublicEndpointClosureBaseline(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, baseline map[string]string, before func() error) error {
	claim, err := c.databasePublicEndpointClaim(ctx, d, endpoint)
	if err != nil {
		return err
	}
	if stored, ok, parseErr := databasePublicEndpointClaimBaseline(claim, endpoint); parseErr != nil {
		return parseErr
	} else if ok {
		if !reflect.DeepEqual(stored, baseline) {
			return fmt.Errorf("database public endpoint closure baseline changed")
		}
		return nil
	}
	encoded, err := json.Marshal(baseline)
	if err != nil || len(encoded) > 4096 {
		return fmt.Errorf("database public endpoint closure baseline is invalid")
	}
	if err = before(); err != nil {
		return err
	}
	updated := claim.DeepCopy()
	updated.Data[databasePublicEndpointClaimBaselineKey] = string(encoded)
	updated.Data[databasePublicEndpointClaimRevisionKey] = strconv.FormatInt(endpoint.Revision, 10)
	delete(updated.Data, databasePublicEndpointClaimAckKey)
	_, err = c.kube.CoreV1().ConfigMaps(updated.Namespace).Update(ctx, updated, metav1.UpdateOptions{})
	return err
}

func (c *Client) markDatabasePublicEndpointClaim(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, state string, before func() error) error {
	allocations, err := database.PublicEndpointAllocations(endpoint)
	if err != nil {
		return err
	}
	for _, member := range allocations {
		current := endpoint
		current.Allocation = member.Allocation
		if err = c.markOneDatabasePublicEndpointClaim(ctx, d, current, state, before); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) markOneDatabasePublicEndpointClaim(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, state string, before func() error) error {
	claim, err := c.databasePublicEndpointClaim(ctx, d, endpoint)
	if err != nil {
		return err
	}
	if state == databasePublicEndpointClosed && claim.Data[databasePublicEndpointClaimStateKey] == state && claim.Data[databasePublicEndpointClaimRevisionKey] == strconv.FormatInt(endpoint.Revision, 10) && claim.Data[databasePublicEndpointClaimAckKey] == publicTCPHash([]any{}) {
		return nil
	}
	if state == databasePublicEndpointPublished && claim.Data[databasePublicEndpointClaimStateKey] == state && claim.Data[databasePublicEndpointClaimRevisionKey] == "" && claim.Data[databasePublicEndpointClaimBaselineKey] == "" && claim.Data[databasePublicEndpointClaimAckKey] == "" {
		return nil
	}
	if err = before(); err != nil {
		return err
	}
	updated := claim.DeepCopy()
	updated.Data[databasePublicEndpointClaimStateKey] = state
	if state == databasePublicEndpointClosed {
		updated.Data[databasePublicEndpointClaimRevisionKey] = strconv.FormatInt(endpoint.Revision, 10)
		updated.Data[databasePublicEndpointClaimAckKey] = publicTCPHash([]any{})
	} else {
		delete(updated.Data, databasePublicEndpointClaimBaselineKey)
		delete(updated.Data, databasePublicEndpointClaimRevisionKey)
		delete(updated.Data, databasePublicEndpointClaimAckKey)
	}
	_, err = c.kube.CoreV1().ConfigMaps(updated.Namespace).Update(ctx, updated, metav1.UpdateOptions{})
	return err
}

// PrepareDatabasePublicEndpoint keeps the allocation claim but closes any old
// route before certificate, policy or ACL changes are made.
func (c *Client) PrepareDatabasePublicEndpoint(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, before func() error) error {
	if c.dynamic == nil || c.kube == nil {
		return fmt.Errorf("database public endpoints require Kubernetes and the HAProxy TCP API")
	}
	if _, err := c.publicTCPController(ctx); err != nil {
		return err
	}
	if err := c.reserveDatabasePublicEndpoint(ctx, d, endpoint, before); err != nil {
		return err
	}
	return c.closeDatabasePublicEndpointRoute(ctx, d, endpoint, before)
}

func (c *Client) closeDatabasePublicEndpointRoute(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, before func() error) error {
	api := c.dynamic.Resource(publicTCPResource).Namespace(DatabaseNamespace(d.ID))
	object, err := api.Get(ctx, databasePublicEndpointName(endpoint.ID), metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	claim, err := c.databasePublicEndpointClaim(ctx, d, endpoint)
	if err != nil {
		return err
	}
	baseline, pending, err := databasePublicEndpointClaimBaseline(claim, endpoint)
	if err != nil {
		return err
	}
	if claim.Data[databasePublicEndpointClaimStateKey] == databasePublicEndpointClosed && claim.Data[databasePublicEndpointClaimRevisionKey] == strconv.FormatInt(endpoint.Revision, 10) && claim.Data[databasePublicEndpointClaimAckKey] == publicTCPHash([]any{}) {
		if object == nil {
			return nil
		}
		models, _, _ := unstructured.NestedSlice(object.Object, "spec")
		if len(models) == 0 && object.GetAnnotations()["hakopod.io/tcp-acknowledged"] == publicTCPHash([]any{}) {
			return nil
		}
	}
	if object == nil {
		if !pending {
			if claim.Data[databasePublicEndpointClaimStateKey] != databasePublicEndpointNeverPublished {
				return fmt.Errorf("database public endpoint route disappeared without a durable closure baseline")
			}
			states, probeErr := c.publicTCPPokePrefix(ctx, databasePublicEndpointPrefix(d, endpoint))
			if probeErr != nil {
				return probeErr
			}
			for _, state := range states {
				if probeErr = validateDatabasePublicTCPRuntime(d, endpoint, []any{}, state); probeErr != nil {
					return fmt.Errorf("an untracked database public endpoint route is still active: %w", probeErr)
				}
			}
			return c.markDatabasePublicEndpointClaim(ctx, d, endpoint, databasePublicEndpointClosed, before)
		}
		return c.acknowledgeDatabasePublicTCP(ctx, d, endpoint, []any{}, baseline, before)
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || !databasePublicEndpointOwned(object, d, endpoint, string(ns.UID)) {
		return fmt.Errorf("database public endpoint route ownership changed")
	}
	models, _, _ := unstructured.NestedSlice(object.Object, "spec")
	if !pending {
		if len(models) == 0 {
			return fmt.Errorf("database public endpoint route is empty without a durable closure baseline")
		}
		baseline, err = c.databasePublicTCPBaseline(ctx, d, endpoint)
		if err != nil {
			return err
		}
		if err = c.persistDatabasePublicEndpointClosureBaseline(ctx, d, endpoint, baseline, before); err != nil {
			return err
		}
	}
	if len(models) > 0 {
		if err = before(); err != nil {
			return err
		}
		object.Object["spec"] = []any{}
		publicTCPSetPending(object, baseline)
		if _, err = api.Update(ctx, object, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return c.acknowledgeDatabasePublicTCP(ctx, d, endpoint, []any{}, baseline, before)
}

func (c *Client) ReconcileDatabasePublicEndpoint(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, before func() error) error {
	if err := c.ValidateDatabasePublicEndpoint(ctx, d, endpoint); err != nil {
		return err
	}
	if err := c.reserveDatabasePublicEndpoint(ctx, d, endpoint, before); err != nil {
		return err
	}
	if d.Spec.Engine == "redis" {
		if err := c.reconcileRedisPublicMemberServices(ctx, d, endpoint, true, before); err != nil {
			return err
		}
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.Labels[databaseOwner] != d.ID || ns.Labels[managedBy] != "hakopod" {
		return fmt.Errorf("database public endpoint namespace ownership changed")
	}
	desired, err := databasePublicEndpointObject(d, endpoint, string(ns.UID), c.options.IngressClass)
	if err != nil {
		return err
	}
	wanted, _, _ := unstructured.NestedSlice(desired.Object, "spec")
	api := c.dynamic.Resource(publicTCPResource).Namespace(ns.Name)
	current, err := api.Get(ctx, desired.GetName(), metav1.GetOptions{})
	if err == nil && !databasePublicEndpointOwned(current, d, endpoint, string(ns.UID)) {
		return fmt.Errorf("database public endpoint route ownership changed")
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && reflect.DeepEqual(current.Object["spec"], desired.Object["spec"]) && current.GetAnnotations()["hakopod.io/tcp-acknowledged"] == publicTCPHash(wanted) {
		return c.markDatabasePublicEndpointClaim(ctx, d, endpoint, databasePublicEndpointPublished, before)
	}
	if baseline, pending, parseErr := databasePublicEndpointPendingReload(current, desired, wanted); parseErr != nil {
		return parseErr
	} else if pending {
		// Resume the exact accepted reload. Taking a new baseline here could
		// accept the already-mutated worker and would make a retry flap the route.
		return c.acknowledgeDatabasePublicTCP(ctx, d, endpoint, wanted, baseline, before)
	}
	baseline, err := c.databasePublicTCPBaseline(ctx, d, endpoint)
	if err != nil {
		return err
	}
	publicTCPSetPending(desired, baseline)
	if err = before(); err != nil {
		return err
	}
	if current == nil {
		_, err = api.Create(ctx, desired, metav1.CreateOptions{})
	} else {
		current.Object["spec"] = desired.Object["spec"]
		annotations := current.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		for key, value := range desired.GetAnnotations() {
			annotations[key] = value
		}
		delete(annotations, "hakopod.io/tcp-acknowledged")
		current.SetAnnotations(annotations)
		_, err = api.Update(ctx, current, metav1.UpdateOptions{})
	}
	if err != nil {
		return err
	}
	return c.acknowledgeDatabasePublicTCP(ctx, d, endpoint, wanted, baseline, before)
}

func databasePublicEndpointPendingReload(current, desired *unstructured.Unstructured, wanted []any) (map[string]string, bool, error) {
	if current == nil || desired == nil || !reflect.DeepEqual(current.Object["spec"], desired.Object["spec"]) || current.GetAnnotations()["hakopod.io/tcp-acknowledged"] == publicTCPHash(wanted) {
		return nil, false, nil
	}
	baseline, err := publicTCPStoredBaseline(current)
	if err != nil {
		return nil, false, err
	}
	return baseline, true, nil
}

func (c *Client) RemoveDatabasePublicEndpoint(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, before func() error) error {
	if c.dynamic == nil || c.kube == nil {
		return fmt.Errorf("database public endpoints require Kubernetes and the HAProxy TCP API")
	}
	if _, err := c.publicTCPController(ctx); err != nil {
		return err
	}
	if err := c.closeDatabasePublicEndpointRoute(ctx, d, endpoint, before); err != nil {
		return err
	}
	if d.Spec.Engine == "mongodb" {
		if err := c.reconcileMongoDBPublicEndpoint(ctx, d, endpoint, false, before); err != nil {
			return err
		}
	}
	if d.Spec.Engine == "redis" {
		if err := c.reconcileRedisPublicMemberServices(ctx, d, endpoint, false, before); err != nil {
			return err
		}
	}
	api := c.dynamic.Resource(publicTCPResource).Namespace(DatabaseNamespace(d.ID))
	current, err := api.Get(ctx, databasePublicEndpointName(endpoint.ID), metav1.GetOptions{})
	if err == nil {
		ns, e := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
		if e != nil || !databasePublicEndpointOwned(current, d, endpoint, string(ns.UID)) {
			return fmt.Errorf("database public endpoint route ownership changed")
		}
		if err = before(); err != nil {
			return err
		}
		uid := current.GetUID()
		if err = api.Delete(ctx, current.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	// Keep the claim as durable closure proof until certificate and policy
	// cleanup has completed and the worker has recorded the release phase.
	return nil
}

// ReleaseDatabasePublicEndpoint removes the allocation claim only after the
// durable operation has reached its release phase. A missing claim is then an
// idempotent retry, rather than ambiguous evidence that a route was closed.
func (c *Client) ReleaseDatabasePublicEndpoint(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, before func() error) error {
	allocations, err := database.PublicEndpointAllocations(endpoint)
	if err != nil {
		return err
	}
	claims := make([]*corev1.ConfigMap, 0, len(allocations))
	for _, member := range allocations {
		current := endpoint
		current.Allocation = member.Allocation
		claim, getErr := c.kube.CoreV1().ConfigMaps(c.options.ProxyNamespace).Get(ctx, publicTCPClaimName(member.Allocation.Port), metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			continue
		}
		if getErr != nil {
			return getErr
		}
		if err = validateDatabasePublicEndpointClaim(claim, d, current); err != nil {
			return err
		}
		if claim.Data[databasePublicEndpointClaimStateKey] != databasePublicEndpointClosed || claim.Data[databasePublicEndpointClaimRevisionKey] != strconv.FormatInt(endpoint.Revision, 10) || claim.Data[databasePublicEndpointClaimAckKey] != publicTCPHash([]any{}) {
			return fmt.Errorf("database public endpoint claim has no durable closure acknowledgement")
		}
		claims = append(claims, claim)
	}
	// Validate the complete remaining set before deleting any claim. Once the
	// durable worker reaches release, an interrupted delete may leave a strict
	// subset; retries accept only missing claims and revalidate every survivor.
	for _, claim := range claims {
		if err = before(); err != nil {
			return err
		}
		if err = c.kube.CoreV1().ConfigMaps(claim.Namespace).Delete(ctx, claim.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &claim.UID}}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (c *Client) ObserveDatabasePublicEndpoint(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) (database.PublicEndpointObservation, error) {
	result := database.PublicEndpointObservation{Message: "The reviewed route is not configured."}
	wanted, err := databasePublicEndpointModels(d, endpoint)
	if err != nil {
		return result, err
	}
	object, err := c.dynamic.Resource(publicTCPResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, databasePublicEndpointName(endpoint.ID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || !databasePublicEndpointOwned(object, d, endpoint, string(ns.UID)) {
		return result, fmt.Errorf("database public endpoint route ownership changed")
	}
	models, _, _ := unstructured.NestedSlice(object.Object, "spec")
	if reflect.DeepEqual(models, wanted) && object.GetAnnotations()["hakopod.io/tcp-acknowledged"] == publicTCPHash(wanted) {
		stamp := time.Now().UTC()
		result.Configured = true
		result.CheckedAt = &stamp
		result.Message = "Route configured and reload acknowledged. External reachability has not been verified."
		if d.Spec.Engine == "redis" && d.Spec.Mode == "cluster" {
			result.ClientAddressMap, err = c.observeRedisPublicAddressMap(ctx, d, endpoint)
			if err != nil {
				result.Configured = false
				result.ClientAddressMap = nil
				return result, err
			}
			result.Message = "Each public Redis member requires the displayed client address map. External reachability has not been verified."
		}
	}
	return result, nil
}

func (c *Client) databasePublicTCPBaseline(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint) (map[string]string, error) {
	if c.databasePublicTCPAck != nil {
		return nil, nil
	}
	states, err := c.publicTCPPokePrefix(ctx, databasePublicEndpointPrefix(d, endpoint))
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for uid, state := range states {
		result[uid] = state.pid
	}
	return result, nil
}

func (c *Client) acknowledgeDatabasePublicTCP(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, wanted []any, baseline map[string]string, before func() error) error {
	if c.databasePublicTCPAck != nil {
		if err := c.databasePublicTCPAck(ctx, d, endpoint, wanted, baseline); err != nil {
			return err
		}
		return c.markDatabasePublicEndpointClaim(ctx, d, endpoint, map[bool]string{true: databasePublicEndpointClosed, false: databasePublicEndpointPublished}[len(wanted) == 0], before)
	}
	api := c.dynamic.Resource(publicTCPResource).Namespace(DatabaseNamespace(d.ID))
	expectedUID := types.UID("")
	expected, err := api.Get(ctx, databasePublicEndpointName(endpoint.ID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) && len(wanted) == 0 {
		expected = nil
	} else if err != nil {
		return err
	}
	if expected != nil {
		ns, ownErr := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
		actual, _, specErr := unstructured.NestedSlice(expected.Object, "spec")
		stored, baselineErr := publicTCPStoredBaseline(expected)
		if ownErr != nil || !databasePublicEndpointOwned(expected, d, endpoint, string(ns.UID)) || specErr != nil || baselineErr != nil || !reflect.DeepEqual(actual, wanted) || !reflect.DeepEqual(stored, baseline) {
			return fmt.Errorf("database public endpoint configuration changed before reload acknowledgement")
		}
		if len(wanted) > 0 && expected.GetAnnotations()["hakopod.io/database-endpoint-revision"] != strconv.FormatInt(endpoint.Revision, 10) {
			return fmt.Errorf("database public endpoint revision changed before reload acknowledgement")
		}
		expectedUID = expected.GetUID()
		if expectedUID == "" {
			return fmt.Errorf("database public endpoint route has no stable identity")
		}
	}
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var last error
	for {
		states, err := c.publicTCPPokePrefix(wait, databasePublicEndpointPrefix(d, endpoint))
		if err == nil {
			if len(states) != len(baseline) {
				err = fmt.Errorf("waiting for every owned HAProxy worker to reload")
			}
			for uid, state := range states {
				if err != nil {
					break
				}
				if previous, exists := baseline[uid]; exists && state.pid == previous {
					err = fmt.Errorf("waiting for the active HAProxy worker to reload")
					break
				}
				if err = validateDatabasePublicTCPRuntime(d, endpoint, wanted, state); err != nil {
					break
				}
			}
		}
		if err == nil && len(wanted) == 0 {
			err = c.closeDatabasePublicEndpointSessions(wait, d, endpoint, baseline, before)
		}
		if err == nil {
			return c.persistDatabasePublicEndpointAcknowledgement(wait, d, endpoint, wanted, baseline, expectedUID, before)
		}
		if wait.Err() == nil || last == nil {
			last = err
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("database public endpoint reload was not acknowledged: %w (last check: %v)", wait.Err(), last)
		case <-ticker.C:
		}
	}
}

func (c *Client) persistDatabasePublicEndpointAcknowledgement(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, wanted []any, baseline map[string]string, expectedUID types.UID, before func() error) error {
	api := c.dynamic.Resource(publicTCPResource).Namespace(DatabaseNamespace(d.ID))
	object, err := api.Get(ctx, databasePublicEndpointName(endpoint.ID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) && len(wanted) == 0 {
		if expectedUID != "" {
			return fmt.Errorf("database public endpoint route disappeared before reload acknowledgement")
		}
		return c.markDatabasePublicEndpointClaim(ctx, d, endpoint, databasePublicEndpointClosed, before)
	}
	if err != nil {
		return err
	}
	ns, ownErr := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	actual, _, specErr := unstructured.NestedSlice(object.Object, "spec")
	stored, baselineErr := publicTCPStoredBaseline(object)
	if expectedUID == "" || object.GetUID() != expectedUID || ownErr != nil || !databasePublicEndpointOwned(object, d, endpoint, string(ns.UID)) || specErr != nil || baselineErr != nil || !reflect.DeepEqual(actual, wanted) || !reflect.DeepEqual(stored, baseline) {
		return fmt.Errorf("database public endpoint configuration changed before reload acknowledgement")
	}
	if len(wanted) > 0 && object.GetAnnotations()["hakopod.io/database-endpoint-revision"] != strconv.FormatInt(endpoint.Revision, 10) {
		return fmt.Errorf("database public endpoint revision changed before reload acknowledgement")
	}
	annotations := object.GetAnnotations()
	annotations["hakopod.io/tcp-acknowledged"] = publicTCPHash(wanted)
	object.SetAnnotations(annotations)
	// Fence the write after all Kubernetes reads and validation. The operation
	// lease or caller authority may expire while those reads are in progress.
	if err = before(); err != nil {
		return err
	}
	if _, err = api.Update(ctx, object, metav1.UpdateOptions{}); err != nil {
		return err
	}
	state := databasePublicEndpointPublished
	if len(wanted) == 0 {
		state = databasePublicEndpointClosed
	}
	return c.markDatabasePublicEndpointClaim(ctx, d, endpoint, state, before)
}

func validateDatabasePublicTCPRuntime(d database.Resource, endpoint database.PublicEndpoint, wanted []any, state publicTCPRuntime) error {
	sections := map[string][]string{}
	name := ""
	for _, line := range strings.Split(state.configuration, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "frontend" && len(fields) == 2 {
			name = fields[1]
			sections[name] = []string{}
			continue
		}
		if name != "" {
			sections[name] = append(sections[name], strings.Join(fields, " "))
		}
	}
	if len(sections) != len(wanted) || len(state.active) != len(wanted) {
		return fmt.Errorf("generated and active database TCP frontend counts do not match the reviewed listener")
	}
	if len(wanted) == 0 {
		return nil
	}
	allocations, err := database.PublicEndpointAllocations(endpoint)
	if err != nil {
		return err
	}
	backend, err := databasePublicEndpointBackend(d, endpoint.Spec.Purpose)
	if err != nil {
		return err
	}
	for index, raw := range wanted {
		model, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("reviewed database TCP model is invalid")
		}
		modelName, _ := model["name"].(string)
		service, _, _ := unstructured.NestedString(model, "service", "name")
		frontend := "tcpcr_" + DatabaseNamespace(d.ID) + "_" + modelName
		lines, exists := sections[frontend]
		if !exists || !slices.Contains(state.active, frontend) {
			return fmt.Errorf("reviewed database TCP frontend has not become active")
		}
		expected := []string{"mode tcp", fmt.Sprintf("maxconn %d", endpoint.Spec.MaxConnections), "timeout client 300000", fmt.Sprintf("bind 0.0.0.0:%d name v4", allocations[index].Allocation.Port), "acl allowed_source src " + strings.Join(endpoint.Spec.SourceCIDRs, " "), "tcp-request connection reject unless allowed_source", "default_backend " + DatabaseNamespace(d.ID) + "_svc_" + service + "_" + backend.BackendPortName}
		for _, line := range expected {
			if !slices.Contains(lines, line) {
				return fmt.Errorf("database TCP frontend does not match its reviewed bind, source ACL, connection cap and backend")
			}
		}
		for _, prefix := range []string{"bind ", "acl ", "tcp-request ", "default_backend "} {
			count := 0
			for _, line := range lines {
				if strings.HasPrefix(line, prefix) {
					count++
				}
			}
			if count != 1 {
				return fmt.Errorf("database TCP frontend contains unreviewed routing directives")
			}
		}
		for _, line := range lines {
			if strings.HasPrefix(line, "use_backend ") {
				return fmt.Errorf("database TCP frontend contains unreviewed backend switching")
			}
		}
	}
	return nil
}

// An acknowledged reload can leave an old worker draining existing sessions.
// Address the old worker through the HAProxy master CLI and close only sessions
// attached to this endpoint's frontend; other applications remain untouched.
const databasePublicEndpointSessionClosureScript = `set -eu
frontend="$1"
socket="$2"
[ -S "$socket" ]
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

show_proc() {
  printf 'show proc\n' | socat -t 2 - UNIX-CONNECT:"$socket" > "$work/procs" || return 1
  awk '
    /^#<PID>[[:space:]]+<type>/ {header=1; next}
    /^#/ || NF==0 {next}
    $1 ~ /^[0-9]+$/ && $2=="master" {masters++; next}
    $1 ~ /^[0-9]+$/ && $2=="worker" {workers++; print $1; next}
    {exit 1}
    END {if (!header || masters != 1 || workers < 1 || workers > 16) exit 1}
  ' "$work/procs" > "$work/workers" || return 1
}

worker_exists() {
  awk -v pid="$1" '$1==pid && $2=="worker" {found=1} END {exit !found}' "$work/procs"
}

sessions() {
  pid="$1"
  output="$2"
  if ! printf '@!%s show info\n' "$pid" | socat -t 2 - UNIX-CONNECT:"$socket" > "$work/info" || ! grep -Eq "^Pid: $pid$" "$work/info"; then
    show_proc || return 1
    worker_exists "$pid" || return 2
    return 1
  fi
  if ! printf '@!%s show sess\n' "$pid" | socat -t 2 - UNIX-CONNECT:"$socket" > "$output"; then
    show_proc || return 1
    worker_exists "$pid" || return 2
    return 1
  fi
  [ "$(wc -c < "$output")" -le 1048576 ] || return 1
  awk 'NF && $1 !~ /^0x[0-9A-Fa-f]+:$/ {exit 1}' "$output" || return 1
}

# Repeat from the master inventory so multiple draining generations are all
# visited. If one exits normally between show proc and show sess it needs no
# shutdown; an invalid response from a still-listed worker fails closed.
pass=0
while [ "$pass" -lt 4 ]; do
  pass=$((pass+1))
  show_proc || exit 1
  cp "$work/workers" "$work/pass-workers" || exit 1
  while IFS= read -r pid; do
    [ -n "$pid" ] || continue
    if sessions "$pid" "$work/sessions"; then
      awk -v target="$frontend" 'index($0," fe=" target " ") {gsub(/:$/,"",$1); print $1}' "$work/sessions" > "$work/matches" || exit 1
      [ "$(wc -l < "$work/matches")" -le 256 ] || exit 1
      while IFS= read -r session; do
        [ -n "$session" ] || continue
        printf '@!%s shutdown session %s\n' "$pid" "$session" | socat -t 2 - UNIX-CONNECT:"$socket" > "$work/shutdown" || exit 1
        [ ! -s "$work/shutdown" ] || exit 1
      done < "$work/matches"
    else
      code=$?
      [ "$code" -eq 2 ] || exit "$code"
    fi
  done < "$work/pass-workers"
done

show_proc || exit 1
cp "$work/workers" "$work/final-workers" || exit 1
while IFS= read -r pid; do
  [ -n "$pid" ] || continue
  if sessions "$pid" "$work/remaining"; then
    awk -v target="$frontend" 'index($0," fe=" target " ") {exit 1}' "$work/remaining" || exit 1
  else
    code=$?
    [ "$code" -eq 2 ] || exit "$code"
  fi
done < "$work/final-workers"
printf 'CLOSED\n'
`

func (c *Client) closeDatabasePublicEndpointSessions(ctx context.Context, d database.Resource, endpoint database.PublicEndpoint, baseline map[string]string, before func() error) error {
	if len(baseline) == 0 {
		return nil
	}
	if c.execConfig == nil || c.restClient() == nil {
		return fmt.Errorf("database public endpoint session revocation requires ingress runtime access")
	}
	pods, err := c.kube.CoreV1().Pods(c.options.ProxyNamespace).List(ctx, metav1.ListOptions{FieldSelector: "status.phase=Running", LabelSelector: "app.kubernetes.io/name=kubernetes-ingress,app.kubernetes.io/instance=" + c.options.ProxyRelease, Limit: publicTCPMaxIngressPods})
	if err != nil || pods.Continue != "" || len(pods.Items) > publicTCPMaxIngressPods {
		return fmt.Errorf("database public endpoint session inventory is unavailable")
	}
	allocations, err := database.PublicEndpointAllocations(endpoint)
	if err != nil {
		return err
	}
	frontends := []string{databasePublicEndpointPrefix(d, endpoint)}
	if len(endpoint.MemberAllocations) > 0 {
		frontends = make([]string, len(allocations))
		for i := range allocations {
			frontends[i] = databasePublicEndpointPrefix(d, endpoint) + "-" + strconv.Itoa(i)
		}
	}
	for _, pod := range pods.Items {
		old, ok := baseline[string(pod.UID)]
		if !ok || old == "" {
			continue
		}
		for _, frontend := range frontends {
			if err = before(); err != nil {
				return err
			}
			command := []string{"sh", "-c", databasePublicEndpointSessionClosureScript, "database-public-session-revoke", frontend, "/var/run/haproxy-master.sock"}
			request := c.restClient().Post().Resource("pods").Namespace(pod.Namespace).Name(pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "kubernetes-ingress-controller", Command: command, Stdout: true, Stderr: true}, scheme.ParameterCodec)
			executor, e := remotecommand.NewSPDYExecutor(c.execConfig, http.MethodPost, request.URL())
			if e != nil {
				return e
			}
			bounded := &tcpBoundedWriter{limit: 4096}
			step, cancel := context.WithTimeout(ctx, 4*time.Second)
			e = executor.StreamWithContext(step, remotecommand.StreamOptions{Stdout: bounded, Stderr: io.Discard})
			cancel()
			if e != nil {
				return fmt.Errorf("close old database endpoint sessions: %w", e)
			}
			if bounded.String() != "CLOSED\n" {
				return fmt.Errorf("close old database endpoint sessions: ingress master CLI did not confirm closure")
			}
		}
	}
	return nil
}
