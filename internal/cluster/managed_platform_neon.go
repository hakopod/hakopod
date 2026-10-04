package cluster

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
)

type NeonRuntimeRequest struct {
	Operation       store.ManagedPlatformOperation
	Render          managedplatform.NeonRenderInput
	SecretSnapshots map[string]map[string][]byte
	ProxyEndpoint   managedplatform.NeonProxyBootstrapState
}

type neonDurableStore interface {
	ManagedPlatformOperationStore
	ActivateNeonProxyEndpoint(context.Context, store.ManagedPlatformOperation, store.NeonProxyEndpointRecord) error
	RevokeNeonProxyEndpoint(context.Context, store.ManagedPlatformOperation) error
}

type neonRecoveryBindingStore interface {
	NeonRecoveryBindingForLifecycle(context.Context, store.ManagedPlatformOperation) (store.NeonRecoveryBinding, error)
}

type neonLifecycleAdapter struct {
	state ManagedPlatformOperationStore
	op    store.ManagedPlatformOperation
}

func (a neonLifecycleAdapter) Operation() managedplatform.DurableOperation {
	return managedplatform.DurableOperation{ID: a.op.ID, PlatformID: a.op.PlatformID, Revision: a.op.Revision, Kind: a.op.Kind}
}
func (a neonLifecycleAdapter) Heartbeat(ctx context.Context) error {
	return a.state.HeartbeatManagedPlatformOperation(ctx, a.op)
}
func (a neonLifecycleAdapter) NeonProviderStateEmpty(ctx context.Context) (bool, error) {
	state, ok := a.state.(interface {
		NeonProviderStateEmpty(context.Context, store.ManagedPlatformOperation) (bool, error)
	})
	if !ok {
		return false, nil
	}
	return state.NeonProviderStateEmpty(ctx, a.op)
}
func (a neonLifecycleAdapter) Claims(ctx context.Context, revision int64) ([]managedplatform.DurableResourceClaim, error) {
	values, err := a.state.PlatformResourceClaims(ctx, a.op, revision)
	if err != nil {
		return nil, err
	}
	out := make([]managedplatform.DurableResourceClaim, 0, len(values))
	for _, v := range values {
		out = append(out, managedplatform.DurableResourceClaim{PlatformID: v.PlatformID, PlatformRevision: v.PlatformRevision, Component: v.Component, Kind: v.Kind, ResourceID: v.ResourceID, ImmutableGeneration: v.ImmutableGeneration, OwnerOperationID: v.OwnerOperationID})
	}
	return out, nil
}
func (a neonLifecycleAdapter) Reserve(ctx context.Context, v managedplatform.DurableResourceIntent) (managedplatform.DurableResourceIntent, error) {
	stored, err := a.state.ReservePlatformResourceIntent(ctx, a.op, store.PlatformResourceIntent{ID: v.ID, PlatformID: v.PlatformID, PlatformRevision: v.PlatformRevision, Component: v.Component, Kind: v.Kind, ExternalKey: v.ExternalKey, OwnerOperationID: v.OwnerOperationID})
	if err != nil {
		return managedplatform.DurableResourceIntent{}, err
	}
	return managedplatform.DurableResourceIntent{ID: stored.ID, PlatformID: stored.PlatformID, PlatformRevision: stored.PlatformRevision, Component: stored.Component, Kind: stored.Kind, ExternalKey: stored.ExternalKey, OwnerOperationID: stored.OwnerOperationID, Confirmed: stored.ConfirmedAt != nil}, nil
}
func (a neonLifecycleAdapter) Intents(ctx context.Context, revision int64) ([]managedplatform.DurableResourceIntent, error) {
	values, err := a.state.PlatformResourceIntents(ctx, a.op, revision)
	if err != nil {
		return nil, err
	}
	out := make([]managedplatform.DurableResourceIntent, 0, len(values))
	for _, v := range values {
		out = append(out, managedplatform.DurableResourceIntent{ID: v.ID, PlatformID: v.PlatformID, PlatformRevision: v.PlatformRevision, Component: v.Component, Kind: v.Kind, ExternalKey: v.ExternalKey, OwnerOperationID: v.OwnerOperationID, Confirmed: v.ConfirmedAt != nil})
	}
	return out, nil
}
func (a neonLifecycleAdapter) Confirm(ctx context.Context, i managedplatform.DurableResourceIntent, c managedplatform.DurableResourceClaim) error {
	return a.state.ConfirmPlatformResourceIntent(ctx, a.op, store.PlatformResourceIntent{ID: i.ID, PlatformID: i.PlatformID, PlatformRevision: i.PlatformRevision, Component: i.Component, Kind: i.Kind, ExternalKey: i.ExternalKey, OwnerOperationID: i.OwnerOperationID}, store.PlatformResourceClaim{PlatformID: c.PlatformID, PlatformRevision: c.PlatformRevision, Component: c.Component, Kind: c.Kind, ResourceID: c.ResourceID, ImmutableGeneration: c.ImmutableGeneration, OwnerOperationID: c.OwnerOperationID})
}
func (a neonLifecycleAdapter) Cancel(ctx context.Context, i managedplatform.DurableResourceIntent) error {
	return a.state.CancelPlatformResourceIntent(ctx, a.op, store.PlatformResourceIntent{ID: i.ID, PlatformID: i.PlatformID, PlatformRevision: i.PlatformRevision, Component: i.Component, Kind: i.Kind, ExternalKey: i.ExternalKey, OwnerOperationID: i.OwnerOperationID})
}
func (a neonLifecycleAdapter) Claim(ctx context.Context, c managedplatform.DurableResourceClaim) error {
	return a.state.ClaimPlatformResource(ctx, a.op, store.PlatformResourceClaim{PlatformID: c.PlatformID, PlatformRevision: c.PlatformRevision, Component: c.Component, Kind: c.Kind, ResourceID: c.ResourceID, ImmutableGeneration: c.ImmutableGeneration, OwnerOperationID: c.OwnerOperationID})
}
func (a neonLifecycleAdapter) Advance(ctx context.Context, c managedplatform.DurableResourceClaim) (managedplatform.DurableResourceClaim, error) {
	stored := store.PlatformResourceClaim{PlatformID: c.PlatformID, PlatformRevision: c.PlatformRevision, Component: c.Component, Kind: c.Kind, ResourceID: c.ResourceID, ImmutableGeneration: c.ImmutableGeneration, OwnerOperationID: c.OwnerOperationID}
	if err := a.state.AdvancePlatformResourceClaim(ctx, a.op, stored); err != nil {
		return managedplatform.DurableResourceClaim{}, err
	}
	c.PlatformRevision = a.op.Revision
	c.OwnerOperationID = a.op.ID
	return c, nil
}
func (a neonLifecycleAdapter) Verify(ctx context.Context, c managedplatform.DurableResourceClaim) error {
	return a.state.VerifyPlatformResourceClaim(ctx, a.op, store.PlatformResourceClaim{PlatformID: c.PlatformID, PlatformRevision: c.PlatformRevision, Component: c.Component, Kind: c.Kind, ResourceID: c.ResourceID, ImmutableGeneration: c.ImmutableGeneration, OwnerOperationID: c.OwnerOperationID})
}
func (a neonLifecycleAdapter) Release(ctx context.Context, c managedplatform.DurableResourceClaim) error {
	return a.state.ReleasePlatformResourceClaim(ctx, a.op, store.PlatformResourceClaim{PlatformID: c.PlatformID, PlatformRevision: c.PlatformRevision, Component: c.Component, Kind: c.Kind, ResourceID: c.ResourceID, ImmutableGeneration: c.ImmutableGeneration, OwnerOperationID: c.OwnerOperationID})
}

type NeonRuntimeObservation struct {
	Phase              string   `json:"phase"`
	Status             string   `json:"status"`
	Revision           int64    `json:"revision"`
	NamespaceUID       string   `json:"namespace_uid,omitempty"`
	ReadyComponents    int      `json:"ready_components"`
	ExpectedComponents int      `json:"expected_components"`
	Pending            []string `json:"pending,omitempty"`
}

func (c *Client) neonAvailabilityZones(ctx context.Context, spec managedplatform.Spec) ([]string, error) {
	if spec.Neon == nil {
		return nil, fmt.Errorf("Neon runtime specification is unavailable")
	}
	members := max(3, spec.Neon.Pageservers)
	if len(spec.Placement.NodeNames) < members {
		return nil, fmt.Errorf("Neon availability-zone inventory is unavailable without explicit placement.node_names")
	}
	zones := make([]string, members)
	for i, name := range spec.Placement.NodeNames[:members] {
		node, err := c.kube.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("read Neon placement node %s: %w", name, err)
		}
		ready := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}
		zone := node.Labels["topology.kubernetes.io/zone"]
		if node.Name != name || node.DeletionTimestamp != nil || !ready || len(utilvalidation.IsDNS1123Label(zone)) != 0 {
			return nil, fmt.Errorf("Neon placement node %s has no ready trusted availability-zone identity", name)
		}
		zones[i] = zone
	}
	return zones, nil
}

func (c *Client) neonLifecycleZones(ctx context.Context, kind string, spec managedplatform.Spec) ([]string, error) {
	if kind == "delete" {
		return nil, nil
	}
	return c.neonAvailabilityZones(ctx, spec)
}

// ReconcileNeonOperation creates the complete native runtime inventory in one
// owned namespace. Each attempt is bounded and persists Kubernetes object UIDs
// through the shared PostgreSQL resource-claim contract before returning.
func (c *Client) ReconcileNeonOperation(ctx context.Context, state ManagedPlatformOperationStore, request NeonRuntimeRequest, encryptionKey []byte) error {
	op := request.Operation
	if op.ID == "" || op.Lease == "" || op.PlatformID == "" || op.Revision < 1 || request.Render.PlatformID != op.PlatformID || request.Render.Revision != op.Revision || request.Render.Spec.Kind != "neon" {
		return fmt.Errorf("invalid Neon operation contract")
	}
	before := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return state.HeartbeatManagedPlatformOperation(ctx, op)
	}
	durableStore, ok := state.(neonDurableStore)
	if !ok {
		return fmt.Errorf("Neon runtime requires durable PostgreSQL proxy authority")
	}
	if op.Kind == "delete" {
		if err := durableStore.RevokeNeonProxyEndpoint(ctx, op); err != nil {
			return managedPlatformRuntimeError("neon_proxy_activate", err)
		}
		ns, namespaceErr := c.kube.CoreV1().Namespaces().Get(ctx, "managed-platform-"+op.PlatformID, metav1.GetOptions{})
		if namespaceErr != nil && !apierrors.IsNotFound(namespaceErr) {
			return managedPlatformRuntimeError("neon_namespace", namespaceErr)
		}
		if apierrors.IsNotFound(namespaceErr) || ns.DeletionTimestamp != nil {
			return c.deleteSupabaseOperation(ctx, state, op, before)
		}
		prior, current, err := loadSupabaseClaims(ctx, state, op)
		if err != nil {
			return managedPlatformRuntimeError("neon_claims", err)
		}
		namespaceClaim, claimed := current["namespace."+ns.Name]
		if !claimed {
			namespaceClaim, claimed = prior["namespace."+ns.Name]
		}
		if claimed && namespaceClaim.ResourceID != string(ns.UID) {
			return fmt.Errorf("Neon deletion namespace claim identity changed")
		}
		// Initial provisioning can fail before managed TLS or compute authority
		// is attached. Only a fenced all-revision absence proof permits owned
		// Kubernetes cleanup without preparing those provider credentials.
		empty, err := (neonLifecycleAdapter{state: state, op: op}).NeonProviderStateEmpty(ctx)
		if err != nil {
			return managedPlatformRuntimeError("neon_provider_state", err)
		}
		if empty {
			if err = before(); err != nil {
				return err
			}
			return c.deleteSupabaseOperation(ctx, state, op, before)
		}
		if err = c.repairTerminalPlatformRuntime(ctx, state, op, ns, prior, before); err != nil {
			return managedPlatformRuntimeError("neon_runtime_repair", err)
		}
		if request.Render.Spec.TLSMode == "managed" {
			if err = c.prepareManagedPlatformTLS(ctx, state, op, ns, &request.Render.Spec, &request.Render.PreviousSpec, &request.SecretSnapshots, prior, current, before); err != nil {
				return managedPlatformRuntimeError("neon_tls_prepare", err)
			}
		}
		zones, err := c.neonLifecycleZones(ctx, op.Kind, request.Render.Spec)
		if err != nil {
			return err
		}
		lifecycle, runtimeRequest, _, err := prepareNeonLifecycle(ctx, request, state, encryptionKey, zones)
		if err != nil {
			return managedPlatformRuntimeError("neon_lifecycle_prepare", err)
		}
		if err = lifecycle.Deprovision(ctx, runtimeRequest); err != nil {
			return managedPlatformRuntimeError("neon_lifecycle_deprovision", err)
		}
		return c.deleteSupabaseOperation(ctx, state, op, before)
	}
	if op.Kind != "create" && op.Kind != "update" {
		return fmt.Errorf("unsupported Neon operation kind")
	}
	zones, err := c.neonLifecycleZones(ctx, op.Kind, request.Render.Spec)
	if err != nil {
		return managedPlatformRuntimeError("neon_node_inventory", err)
	}
	if bindings, ok := state.(neonRecoveryBindingStore); ok {
		binding, bindingErr := bindings.NeonRecoveryBindingForLifecycle(ctx, op)
		if bindingErr == nil {
			request.Render.RecoveryStoragePrefix = strings.TrimSuffix(binding.StagingPrefix, "/")
		} else if !errors.Is(bindingErr, pgx.ErrNoRows) {
			return managedPlatformRuntimeError("neon_recovery_binding", bindingErr)
		}
	}
	prior, current, err := loadSupabaseClaims(ctx, state, op)
	if err != nil {
		return managedPlatformRuntimeError("neon_claims", err)
	}
	ns, err := c.ensureSupabaseNamespace(ctx, state, op, prior, current, before)
	if err != nil {
		return managedPlatformRuntimeError("neon_namespace", err)
	}
	request.Render.NamespaceUID = ns.UID
	if err = c.repairTerminalPlatformRuntime(ctx, state, op, ns, prior, before); err != nil {
		return managedPlatformRuntimeError("neon_runtime_repair", err)
	}
	if err = c.prepareManagedPlatformTLS(ctx, state, op, ns, &request.Render.Spec, &request.Render.PreviousSpec, &request.SecretSnapshots, prior, current, before); err != nil {
		return managedPlatformRuntimeError("neon_tls_prepare", err)
	}
	lifecycle, runtimeRequest, route, err := prepareNeonLifecycle(ctx, request, state, encryptionKey, zones)
	if err != nil {
		return managedPlatformRuntimeError("neon_lifecycle_prepare", err)
	}
	if err = prepareNeonControllerSecret(&request, encryptionKey); err != nil {
		return managedPlatformRuntimeError("neon_controller_secret", err)
	}
	manifests, err := managedplatform.RenderNeon(request.Render)
	if err != nil {
		return managedPlatformRuntimeError("neon_render", err)
	}
	if op.Spec.TLSMode == "managed" {
		manifests.RetainSecretSnapshots = append(manifests.RetainSecretSnapshots, managedplatform.ManagedTLSIssuerSecret)
	}
	if err = validateNeonSecretSnapshot(request.SecretSnapshots, manifests.RequiredSecrets, request.Render.Spec, request.Render.PlatformID); err != nil {
		return managedPlatformRuntimeError("neon_secret_validate", err)
	}
	for _, name := range manifests.RequiredSecrets {
		if err = c.applySupabaseSecret(ctx, state, op, ns, name, request.SecretSnapshots[name], prior, current, before); err != nil {
			return managedPlatformRuntimeError("neon_secret_apply", err)
		}
	}
	objects := append([]runtime.Object(nil), manifests.Objects...)
	sort.SliceStable(objects, func(i, j int) bool { return supabaseApplyRank(objects[i]) < supabaseApplyRank(objects[j]) })
	for _, object := range objects {
		if err = c.applySupabaseObject(ctx, op, ns, object, state, prior, current, before); err != nil {
			return managedPlatformRuntimeError("neon_object_apply", err)
		}
	}
	bootstrap, err := c.observeNeon(ctx, op, manifests, current, false)
	if err != nil {
		return managedPlatformRuntimeError("neon_bootstrap_observe", err)
	}
	if bootstrap.Status != "ready" {
		return state.RecordManagedPlatformStep(ctx, op, "queued", "waiting-bootstrap", "Neon infrastructure is not ready for lifecycle provisioning.", neonObservationMap(bootstrap))
	}
	if !op.Maintenance {
		if _, err = lifecycle.Provision(ctx, runtimeRequest); err != nil {
			return managedPlatformRuntimeError("neon_lifecycle_provision", err)
		}
	} else if err = c.replayNeonComputes(ctx, state, encryptionKey, op); err != nil {
		return managedPlatformRuntimeError("neon_compute_replay", err)
	}
	observation, err := c.ObserveNeon(ctx, op, manifests, current)
	if err != nil {
		return managedPlatformRuntimeError("neon_serving_observe", err)
	}
	if observation.Status != "ready" {
		return state.RecordManagedPlatformStep(ctx, op, "queued", "waiting-ready", "Neon serving components are not ready.", neonObservationMap(observation))
	}
	result := neonObservationMap(observation)
	if op.Spec.TLSMode == "managed" {
		tlsObservation, err := c.observeManagedPlatformTLS(ctx, state, op, ns, request.Render.Spec, request.SecretSnapshots, current, before)
		if err != nil {
			return managedPlatformRuntimeError("platform_tls_observe", err)
		}
		result["tls"] = tlsObservation
	}
	prune := managedplatform.SupabaseManifests{PruneConfigMapsBeforeRevision: manifests.PruneConfigMapsBeforeRevision, RetainSecretSnapshots: manifests.RetainSecretSnapshots}
	if err = c.pruneSupabaseSnapshots(ctx, state, op, ns, prune, prior, current, before); err != nil {
		return managedPlatformRuntimeError("neon_snapshot_prune", err)
	}
	if op.Maintenance {
		return state.RecordManagedPlatformStep(ctx, op, "succeeded", "runtime-ready", "Neon runtime maintenance is verified.", result)
	}
	// A second pass must re-observe every durable identity without issuing a new
	// create before the proxy route becomes visible.
	recovered, err := lifecycle.Provision(ctx, runtimeRequest)
	if err != nil {
		return managedPlatformRuntimeError("neon_recovery_observe", err)
	}
	if !recovered.Complete {
		return fmt.Errorf("Neon recovery observation did not complete")
	}
	sealedRoles, err := managedplatform.SealNeonProxyRoles(encryptionKey, op.PlatformID, op.Revision, route.EndpointID, route.Roles)
	if err != nil {
		return err
	}
	record := store.NeonProxyEndpointRecord{EndpointID: route.EndpointID, PlatformID: op.PlatformID, PlatformRevision: op.Revision, OwnerOperationID: op.ID, Generation: op.Revision, Enabled: route.Enabled, Address: route.Address, ServerName: route.ServerName, ProjectID: route.ProjectID, BranchID: route.BranchID, ComputeID: route.ComputeID, EncryptedRoles: sealedRoles}
	if err = durableStore.ActivateNeonProxyEndpoint(ctx, op, record); err != nil {
		return managedPlatformRuntimeError("neon_proxy_activate", err)
	}
	result["tenant_id"] = recovered.TenantID
	result["timeline_id"] = recovered.TimelineID
	result["safekeeper_count"] = recovered.SafekeeperCount
	result["attached_computes"] = append([]string(nil), recovered.AttachedComputes...)
	result["proxy_endpoint_id"] = route.EndpointID
	result["proxy_generation"] = op.Revision
	return state.RecordManagedPlatformStep(ctx, op, "succeeded", "ready", "Neon runtime and durable proxy route are ready.", result)
}

func prepareNeonLifecycle(ctx context.Context, request NeonRuntimeRequest, state ManagedPlatformOperationStore, encryptionKey []byte, zones []string) (*managedplatform.DurableNeonRuntime, managedplatform.NeonLifecycleRequest, managedplatform.NeonProxyEndpointState, error) {
	return prepareNeonLifecycleWithAdapter(ctx, request, neonLifecycleAdapter{state: state, op: request.Operation}, state, encryptionKey, zones)
}

func prepareNeonLifecycleWithAdapter(ctx context.Context, request NeonRuntimeRequest, lifecycle managedplatform.DurableLifecycle, bindings any, encryptionKey []byte, zones []string) (*managedplatform.DurableNeonRuntime, managedplatform.NeonLifecycleRequest, managedplatform.NeonProxyEndpointState, error) {
	var lifecycleRequest managedplatform.NeonLifecycleRequest
	op := request.Operation
	namespace := "managed-platform-" + op.PlatformID
	if request.Render.Spec.Neon == nil {
		return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, fmt.Errorf("Neon runtime specification is unavailable")
	}
	if request.ProxyEndpoint.EndpointID != op.PlatformID || (op.Kind != "delete" && (!request.ProxyEndpoint.Enabled || len(request.ProxyEndpoint.Roles) == 0 || len(request.ProxyEndpoint.Roles) > 64)) {
		return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, fmt.Errorf("Neon proxy bootstrap does not match the managed platform")
	}
	secret := func(logical, key string) ([]byte, error) {
		ref, ok := request.Render.Spec.Secrets[logical]
		if !ok {
			return nil, fmt.Errorf("Neon secret reference %s is unavailable", logical)
		}
		values, ok := request.SecretSnapshots[ref.Name+"-r"+strconv.FormatInt(ref.Revision, 10)]
		if !ok || len(values[key]) == 0 {
			return nil, fmt.Errorf("Neon secret %s is missing required key %s", logical, key)
		}
		return append([]byte(nil), values[key]...), nil
	}
	controllerToken, err := secret("controller-auth", "token")
	if err != nil {
		return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, err
	}
	computeToken, err := secret("compute-auth", "token")
	if err != nil {
		return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, err
	}
	safekeeperToken, err := secret("safekeeper-auth", "token")
	if err != nil {
		return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, err
	}
	var pageserverToken []byte
	if _, present := request.Render.Spec.Secrets["pageserver-auth"]; present || op.Kind != "delete" {
		pageserverToken, err = secret("pageserver-auth", "token")
		if err != nil {
			return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, err
		}
	}
	roots := x509.NewCertPool()
	trust := []string{"controller-auth", "compute-auth", "safekeeper-auth"}
	if len(pageserverToken) > 0 {
		trust = append(trust, "pageserver-auth")
	}
	for _, logical := range trust {
		pem, readErr := secret(logical, "ca.crt")
		if readErr != nil {
			return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, readErr
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, fmt.Errorf("Neon secret %s contains an invalid CA bundle", logical)
		}
	}
	deprovisionOnly := op.Kind == "delete"
	config := managedplatform.NeonRuntimeConfig{StorageController: managedplatform.NeonControlTarget{Name: "storage-controller", Origin: "https://neon-storage-controller." + namespace + ".svc:6699", Token: string(controllerToken)}, SafekeeperToken: string(safekeeperToken), PageserverToken: string(pageserverToken), RequestTimeout: 20 * time.Second, RootCAs: roots, DeprovisionOnly: deprovisionOnly}
	if reader, ok := bindings.(neonControllerStateReader); ok && !deprovisionOnly {
		config.ResolveComputeConfig = neonControllerComputeResolver(reader, op.PlatformID, op.Revision, request.Render.Spec.Neon.Pageservers)
	}
	if !deprovisionOnly && len(zones) < max(3, request.Render.Spec.Neon.Pageservers) {
		return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, fmt.Errorf("Neon availability-zone inventory is unavailable")
	}
	zone := func(i int) string {
		if deprovisionOnly {
			return ""
		}
		return zones[i%len(zones)]
	}
	for i := 0; i < request.Render.Spec.Neon.Pageservers; i++ {
		name := strconv.Itoa(i)
		config.Pageservers = append(config.Pageservers, managedplatform.NeonPageserverRegistration{Name: name, NodeID: int64(i + 1), Generation: 1, Host: "neon-pageserver-" + name + "." + namespace + ".svc", AvailabilityZone: zone(i)})
	}
	for i := 0; i < request.Render.Spec.Neon.Safekeepers; i++ {
		name := strconv.Itoa(i)
		config.Safekeepers = append(config.Safekeepers, managedplatform.NeonSafekeeperRegistration{Name: name, NodeID: int64(i + 1), Generation: 1, Host: "neon-safekeeper-" + name + "." + namespace + ".svc", AvailabilityZone: zone(i)})
	}
	tenantID := neonDeterministicID(op.PlatformID, "tenant")
	timelineID := neonDeterministicID(op.PlatformID, "timeline")
	if recoveryBindings, ok := bindings.(neonRecoveryBindingStore); ok {
		binding, bindingErr := recoveryBindings.NeonRecoveryBindingForLifecycle(ctx, op)
		if bindingErr == nil {
			tenantID, timelineID = binding.TenantID, binding.TimelineID
		} else if !errors.Is(bindingErr, pgx.ErrNoRows) {
			return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, fmt.Errorf("read Neon recovery identity binding: %w", bindingErr)
		}
	}
	safekeepers := make([]string, 0, len(config.Safekeepers))
	for _, node := range config.Safekeepers {
		safekeepers = append(safekeepers, node.Host+":5454")
	}
	template, err := secret("compute-auth", "config.json")
	if err != nil {
		return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, err
	}
	lifecycleRequest = managedplatform.NeonLifecycleRequest{OperationID: op.ID, TenantID: tenantID, TimelineID: timelineID, CreateTenant: true, ComputeConfig: map[string]json.RawMessage{}}
	for i := 0; i < request.Render.Spec.Neon.ComputeReplicas; i++ {
		name := "compute-" + strconv.Itoa(i)
		controlHost := "neon-" + name + "-control." + namespace + ".svc"
		config.Computes = append(config.Computes, managedplatform.NeonControlTarget{Name: name, Origin: "https://" + controlHost + ":3081", Token: string(computeToken)})
		raw, configErr := bindNeonComputeConfig(template, tenantID, timelineID, safekeepers, name)
		if configErr != nil {
			return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, configErr
		}
		if !deprovisionOnly {
			raw, configErr = managedplatform.BindNeonTenantAuthentication(raw, encryptionKey, op.PlatformID, tenantID)
			if configErr != nil {
				return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, configErr
			}
		}
		lifecycleRequest.ComputeConfig[name] = raw
	}
	durable, err := managedplatform.NewDurableNeonRuntime(config, lifecycle)
	if err != nil {
		return nil, lifecycleRequest, managedplatform.NeonProxyEndpointState{}, err
	}
	route := managedplatform.NeonProxyEndpointState{EndpointID: request.ProxyEndpoint.EndpointID, Enabled: request.ProxyEndpoint.Enabled, Roles: request.ProxyEndpoint.Roles, Address: "neon-compute-0." + namespace + ".svc:55433", ServerName: "neon-compute-0." + namespace + ".svc", ProjectID: op.PlatformID, BranchID: timelineID, ComputeID: "compute-0"}
	return durable, lifecycleRequest, route, nil
}

func neonDeterministicID(platformID, purpose string) string {
	sum := sha256.Sum256([]byte(platformID + "\x00" + purpose))
	return hex.EncodeToString(sum[:16])
}

func bindNeonComputeConfig(template []byte, tenantID, timelineID string, safekeepers []string, computeName string) (json.RawMessage, error) {
	ordinal, err := strconv.Atoi(strings.TrimPrefix(computeName, "compute-"))
	if err != nil || ordinal < 0 || ordinal > 5 || computeName != "compute-"+strconv.Itoa(ordinal) {
		return nil, fmt.Errorf("Neon compute identity is invalid")
	}
	return managedplatform.BindNeonComputeRuntime(template, tenantID, timelineID, safekeepers, ordinal > 0)
}

func validateNeonSecretSnapshot(values map[string]map[string][]byte, names []string, spec managedplatform.Spec, platformID string) error {
	if len(values) != len(names) {
		return fmt.Errorf("resolved Neon secret snapshot is incomplete")
	}
	for _, name := range names {
		data, ok := values[name]
		if !ok || len(data) == 0 || len(data) > 16 {
			return fmt.Errorf("resolved Neon secret %s is unavailable", name)
		}
		total := 0
		for key, value := range data {
			if key == "" || len(value) == 0 {
				return fmt.Errorf("resolved Neon secret %s is invalid", name)
			}
			total += len(key) + len(value)
		}
		if total > 65536 {
			return fmt.Errorf("resolved Neon secret %s exceeds 64 KiB", name)
		}
	}
	required := map[string][]string{
		"broker-auth":                  {"tls.crt", "tls.key", "ca.crt"},
		"compute-auth":                 {"config.json", "token", "tls.crt", "tls.key", "ca.crt"},
		"controller-auth":              {"token", "upcall-token", "public-key.pem", "tls.crt", "tls.key", "ca.crt"},
		"controller-database-password": {"value", "tls.crt", "tls.key", "ca.crt"},
		"object-storage":               {"access-key-id", "secret-access-key"},
		"pageserver-auth":              {"token", "public-key.pem", "tls.crt", "tls.key", "ca.crt"},
		"proxy-auth":                   {"token", "tls.crt", "tls.key"},
		"safekeeper-auth":              {"token", "public-key.pem", "tls.crt", "tls.key", "ca.crt"},
	}
	for logical, keys := range required {
		ref, ok := spec.Secrets[logical]
		if !ok {
			return fmt.Errorf("resolved Neon secret reference %s is unavailable", logical)
		}
		snapshot := ref.Name + "-r" + strconv.FormatInt(ref.Revision, 10)
		data, ok := values[snapshot]
		if !ok {
			return fmt.Errorf("resolved Neon secret snapshot %s is unavailable", snapshot)
		}
		for _, key := range keys {
			if len(data[key]) == 0 {
				return fmt.Errorf("resolved Neon secret %s is missing required key %s", logical, key)
			}
		}
	}
	return validateNeonTLSSecretSnapshots(values, spec, platformID, time.Now())
}

func (c *Client) ObserveNeon(ctx context.Context, op store.ManagedPlatformOperation, manifests managedplatform.NeonManifests, current map[string]store.PlatformResourceClaim) (NeonRuntimeObservation, error) {
	return c.observeNeon(ctx, op, manifests, current, true)
}

func neonBootstrapRequiresReadiness(name string) bool {
	return name == "neon-broker" || name == "neon-controller-database" || name == "neon-storage-controller" || strings.HasPrefix(name, "neon-safekeeper-")
}

func neonDeploymentObserved(item *appsv1.Deployment, serving bool) bool {
	if item == nil || item.Generation != item.Status.ObservedGeneration || item.Spec.Replicas == nil || item.Status.Replicas != *item.Spec.Replicas || item.Status.UpdatedReplicas != *item.Spec.Replicas {
		return false
	}
	return !serving || item.Status.AvailableReplicas == *item.Spec.Replicas
}

func neonStatefulSetObserved(item *appsv1.StatefulSet, serving bool) bool {
	if item == nil || item.Generation != item.Status.ObservedGeneration || item.Spec.Replicas == nil || item.Status.Replicas != *item.Spec.Replicas || item.Status.UpdatedReplicas != *item.Spec.Replicas || item.Status.CurrentRevision != item.Status.UpdateRevision {
		return false
	}
	return !serving || item.Status.ReadyReplicas == *item.Spec.Replicas
}

func (c *Client) observeNeon(ctx context.Context, op store.ManagedPlatformOperation, manifests managedplatform.NeonManifests, current map[string]store.PlatformResourceClaim, serving bool) (NeonRuntimeObservation, error) {
	phase := "bootstrap"
	if serving {
		phase = "serving"
	}
	result := NeonRuntimeObservation{Phase: phase, Status: "ready", Revision: op.Revision}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, manifests.Namespace.Name, metav1.GetOptions{})
	if err != nil {
		return result, err
	}
	if ns.UID != manifests.ExpectedUID || ns.Labels["hakopod.io/managed-platform-id"] != op.PlatformID {
		return result, fmt.Errorf("Neon namespace identity changed")
	}
	if err = verifySupabaseClaimedUID("namespace", ns, current); err != nil {
		return result, err
	}
	result.NamespaceUID = string(ns.UID)
	for _, object := range manifests.Objects {
		switch desired := object.(type) {
		case *corev1.PersistentVolumeClaim:
			result.ExpectedComponents++
			item, e := c.kube.CoreV1().PersistentVolumeClaims(ns.Name).Get(ctx, desired.Name, metav1.GetOptions{})
			if e != nil && !apierrors.IsNotFound(e) {
				return result, e
			}
			if e == nil {
				if e = verifySupabaseOwned(item, op.PlatformID, ns.UID); e != nil {
					return result, e
				}
				if e = verifySupabaseClaimedUID("pvc", item, current); e != nil {
					return result, e
				}
			}
			if e != nil || item.Status.Phase != corev1.ClaimBound {
				result.Pending = append(result.Pending, "pvc/"+desired.Name)
			} else {
				result.ReadyComponents++
			}
		case *appsv1.Deployment:
			result.ExpectedComponents++
			item, e := c.kube.AppsV1().Deployments(ns.Name).Get(ctx, desired.Name, metav1.GetOptions{})
			if e != nil && !apierrors.IsNotFound(e) {
				return result, e
			}
			if e == nil {
				if e = verifySupabaseOwned(item, op.PlatformID, ns.UID); e != nil {
					return result, e
				}
				if e = verifySupabaseClaimedUID("deployment", item, current); e != nil {
					return result, e
				}
				if item.Labels["hakopod.io/revision"] != strconv.FormatInt(op.Revision, 10) {
					return result, fmt.Errorf("Neon Deployment revision changed")
				}
			}
			requireReady := serving || neonBootstrapRequiresReadiness(desired.Name)
			if e != nil || !neonDeploymentObserved(item, requireReady) {
				result.Pending = append(result.Pending, "deployment/"+desired.Name)
			} else {
				result.ReadyComponents++
			}
		case *appsv1.StatefulSet:
			result.ExpectedComponents++
			item, e := c.kube.AppsV1().StatefulSets(ns.Name).Get(ctx, desired.Name, metav1.GetOptions{})
			if e != nil && !apierrors.IsNotFound(e) {
				return result, e
			}
			if e == nil {
				if e = verifySupabaseOwned(item, op.PlatformID, ns.UID); e != nil {
					return result, e
				}
				if e = verifySupabaseClaimedUID("statefulset", item, current); e != nil {
					return result, e
				}
				if item.Labels["hakopod.io/revision"] != strconv.FormatInt(op.Revision, 10) {
					return result, fmt.Errorf("Neon StatefulSet revision changed")
				}
			}
			requireReady := serving || neonBootstrapRequiresReadiness(desired.Name)
			if e != nil || !neonStatefulSetObserved(item, requireReady) {
				result.Pending = append(result.Pending, "statefulset/"+desired.Name)
			} else {
				result.ReadyComponents++
			}
		}
	}
	sort.Strings(result.Pending)
	if len(result.Pending) > 0 {
		result.Status = "pending"
	}
	return result, nil
}

func neonObservationMap(value NeonRuntimeObservation) map[string]any {
	return map[string]any{"phase": value.Phase, "status": value.Status, "revision": value.Revision, "namespace_uid": value.NamespaceUID, "ready_components": value.ReadyComponents, "expected_components": value.ExpectedComponents, "pending": append([]string(nil), value.Pending...)}
}
