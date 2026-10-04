package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

type NeonRecoveryRuntime struct {
	Store                 *store.Store
	Cluster               *Client
	EncryptionKey         []byte
	ValidateQualification func(context.Context, platformbackup.Operation) error
}

const maxNeonRecoveryWorkloads = 33

func (r *NeonRecoveryRuntime) fence(ctx context.Context, op platformbackup.Operation) error {
	if platformbackup.RecoveryCleanupFromContext(ctx) {
		return r.Store.FencePlatformRecoveryCleanup(ctx, op)
	}
	if r.ValidateQualification == nil {
		return fmt.Errorf("Neon recovery qualification is unavailable")
	}
	if err := r.ValidateQualification(ctx, op); err != nil {
		return err
	}
	cancelled, err := r.Store.HeartbeatPlatformRecovery(ctx, op)
	if err != nil {
		return err
	}
	if cancelled {
		return context.Canceled
	}
	return nil
}

func NewNeonRecoveryRuntime(db *store.Store, kube *Client, encryptionKey []byte) (platformbackup.Runtime, error) {
	if db == nil || kube == nil || kube.kube == nil || len(encryptionKey) != 32 {
		return nil, fmt.Errorf("Neon recovery runtime configuration is incomplete")
	}
	return &NeonRecoveryRuntime{Store: db, Cluster: kube, EncryptionKey: append([]byte(nil), encryptionKey...)}, nil
}

type neonRecoveryLifecycle struct {
	runtime   *NeonRecoveryRuntime
	recovery  platformbackup.Operation
	platform  store.ManagedPlatformOperation
	claimMode string
}

func (a neonRecoveryLifecycle) AllowPartialNeonDeprovision() bool { return true }

func (a neonRecoveryLifecycle) Operation() managedplatform.DurableOperation {
	return managedplatform.DurableOperation{ID: a.platform.ID, PlatformID: a.platform.PlatformID, Revision: a.platform.Revision, Kind: a.platform.Kind}
}
func (a neonRecoveryLifecycle) Heartbeat(ctx context.Context) error {
	return a.runtime.fence(ctx, a.recovery)
}
func (a neonRecoveryLifecycle) Claims(ctx context.Context, revision int64) ([]managedplatform.DurableResourceClaim, error) {
	if revision != a.platform.Revision {
		return nil, nil
	}
	if a.recovery.Kind == "backup" {
		values, err := a.runtime.Store.ManagedPlatformRecoveryClaims(ctx, a.platform.PlatformID, a.platform.Revision)
		if err != nil {
			return nil, err
		}
		out := make([]managedplatform.DurableResourceClaim, 0, len(values))
		for _, v := range values {
			out = append(out, managedplatform.DurableResourceClaim{PlatformID: v.PlatformID, PlatformRevision: v.PlatformRevision, Component: v.Component, Kind: v.Kind, ResourceID: v.ResourceID, ImmutableGeneration: v.ImmutableGeneration, OwnerOperationID: v.OwnerOperationID})
		}
		return out, nil
	}
	values, err := a.runtime.Store.EffectiveNeonRecoveryClaims(ctx, a.recovery)
	if err != nil {
		return nil, err
	}
	out := make([]managedplatform.DurableResourceClaim, 0, len(values))
	phases := map[string]string{}
	if a.claimMode != "" {
		resources, resourceErr := a.runtime.Store.NeonRecoveryResources(ctx, a.recovery)
		if resourceErr != nil {
			return nil, resourceErr
		}
		for _, resource := range resources {
			phases[resource.Component] = resource.Phase
		}
	}
	for _, v := range values {
		phase, journaled := phases[v.Component]
		if a.claimMode == "prior" && (!journaled || phase != "planned") {
			continue
		}
		if a.claimMode == "replacement" && (!journaled || phase != "confirmed") {
			continue
		}
		out = append(out, managedplatform.DurableResourceClaim{PlatformID: v.PlatformID, PlatformRevision: v.PlatformRevision, Component: v.Component, Kind: v.Kind, ResourceID: v.ResourceID, ImmutableGeneration: v.ImmutableGeneration, OwnerOperationID: v.OwnerOperationID})
	}
	return out, nil
}
func (a neonRecoveryLifecycle) Reserve(ctx context.Context, intent managedplatform.DurableResourceIntent) (managedplatform.DurableResourceIntent, error) {
	resources, err := a.runtime.Store.NeonRecoveryResources(ctx, a.recovery)
	if err != nil {
		return managedplatform.DurableResourceIntent{}, err
	}
	for _, resource := range resources {
		if resource.Component != intent.Component || resource.Kind != intent.Kind || resource.ReplacementExternalKey != intent.ExternalKey {
			continue
		}
		stored, reserveErr := a.runtime.Store.ReserveNeonRecoveryReplacement(ctx, a.recovery, resource.Component, resource.Kind, resource.ReplacementExternalKey, resource.TransitionToken)
		if reserveErr != nil {
			return managedplatform.DurableResourceIntent{}, reserveErr
		}
		return managedplatform.DurableResourceIntent{ID: stored.TransitionToken, PlatformID: stored.TargetPlatformID, PlatformRevision: stored.TargetRevision, Component: stored.Component, Kind: stored.Kind, ExternalKey: stored.ReplacementExternalKey, OwnerOperationID: a.platform.ID}, nil
	}
	return managedplatform.DurableResourceIntent{}, store.ErrConflict
}
func (a neonRecoveryLifecycle) Intents(ctx context.Context, revision int64) ([]managedplatform.DurableResourceIntent, error) {
	if revision != a.platform.Revision {
		return nil, nil
	}
	if a.recovery.Kind == "backup" {
		return nil, nil
	}
	resources, err := a.runtime.Store.NeonRecoveryResources(ctx, a.recovery)
	if err != nil {
		return nil, err
	}
	out := []managedplatform.DurableResourceIntent{}
	for _, v := range resources {
		if v.Phase == "reserved" {
			out = append(out, managedplatform.DurableResourceIntent{ID: v.TransitionToken, PlatformID: v.TargetPlatformID, PlatformRevision: v.TargetRevision, Component: v.Component, Kind: v.Kind, ExternalKey: v.ReplacementExternalKey, OwnerOperationID: a.platform.ID})
		}
	}
	return out, nil
}
func (a neonRecoveryLifecycle) Confirm(ctx context.Context, intent managedplatform.DurableResourceIntent, claim managedplatform.DurableResourceClaim) error {
	_, err := a.runtime.Store.ConfirmNeonRecoveryReplacement(ctx, a.recovery, intent.Component, claim.ResourceID, claim.ImmutableGeneration, intent.ID)
	return err
}
func (a neonRecoveryLifecycle) Cancel(context.Context, managedplatform.DurableResourceIntent) error {
	return fmt.Errorf("Neon recovery cannot cancel lifecycle resources")
}
func (a neonRecoveryLifecycle) Claim(context.Context, managedplatform.DurableResourceClaim) error {
	return store.ErrConflict
}
func (a neonRecoveryLifecycle) Advance(context.Context, managedplatform.DurableResourceClaim) (managedplatform.DurableResourceClaim, error) {
	return managedplatform.DurableResourceClaim{}, fmt.Errorf("Neon recovery cannot advance resources")
}
func (a neonRecoveryLifecycle) Verify(ctx context.Context, claim managedplatform.DurableResourceClaim) error {
	if err := a.Heartbeat(ctx); err != nil {
		return err
	}
	return a.runtime.Store.VerifyNeonRecoveryResource(ctx, a.recovery, store.PlatformResourceClaim{PlatformID: claim.PlatformID, PlatformRevision: claim.PlatformRevision, Component: claim.Component, Kind: claim.Kind, ResourceID: claim.ResourceID, ImmutableGeneration: claim.ImmutableGeneration, OwnerOperationID: claim.OwnerOperationID})
}
func (a neonRecoveryLifecycle) Release(ctx context.Context, claim managedplatform.DurableResourceClaim) error {
	resources, err := a.runtime.Store.NeonRecoveryResources(ctx, a.recovery)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		if resource.Component == claim.Component && resource.PriorResourceID == claim.ResourceID && resource.PriorGeneration == claim.ImmutableGeneration {
			_, err = a.runtime.Store.MarkNeonRecoveryPriorReleased(ctx, a.recovery, resource.Component, resource.PriorResourceID, resource.PriorGeneration, resource.TransitionToken)
			return err
		}
		if resource.Component == claim.Component && resource.Kind == claim.Kind && resource.Phase == "confirmed" {
			if err = a.Verify(ctx, claim); err != nil {
				return err
			}
			if _, err = a.runtime.Store.MarkNeonRecoveryReplacementReleased(ctx, a.recovery, resource.Component, resource.ReplacementResourceID, resource.ReplacementGeneration, resource.TransitionToken); err != nil {
				return err
			}
			_, err = a.runtime.Store.CompleteNeonRecoveryResource(ctx, a.recovery, resource.Component, resource.TransitionToken)
			return err
		}
	}
	return store.ErrConflict
}

func (r *NeonRecoveryRuntime) contract(ctx context.Context, id string, revision int64) (store.ManagedPlatform, managedplatform.Plan, store.ManagedPlatformOperation, NeonRuntimeRequest, error) {
	item, plan, err := r.Store.ManagedPlatformRecoveryContract(ctx, id, revision)
	if err != nil {
		return item, plan, store.ManagedPlatformOperation{}, NeonRuntimeRequest{}, err
	}
	accepted, err := r.Store.ManagedPlatformRecoverySnapshot(ctx, id, revision)
	if err != nil {
		return item, plan, accepted, NeonRuntimeRequest{}, err
	}
	snapshot, err := OpenManagedPlatformSnapshot(r.EncryptionKey, accepted)
	if err != nil || snapshot.Neon == nil {
		return item, plan, accepted, NeonRuntimeRequest{}, fmt.Errorf("accepted Neon runtime snapshot is unavailable: %w", err)
	}
	req := *snapshot.Neon
	if req.Render.PlatformID != id || req.Render.Revision != revision || !bytes.Equal(store.JSON(req.Render.Spec), store.JSON(item.Spec)) {
		return item, plan, accepted, req, fmt.Errorf("accepted Neon runtime snapshot does not match the platform revision")
	}
	if req.Render.Spec.TLSMode == "managed" {
		claims, err := r.Store.ManagedPlatformRecoveryClaims(ctx, item.ID, item.Revision)
		if err != nil {
			return item, plan, accepted, req, err
		}
		if err = r.Cluster.readManagedPlatformTLS(ctx, item.ID, &req.Render.Spec, &req.Render.PreviousSpec, &req.SecretSnapshots, claims); err != nil {
			return item, plan, accepted, req, err
		}
	}
	return item, plan, accepted, req, nil
}

func (r *NeonRecoveryRuntime) durable(ctx context.Context, recovery platformbackup.Operation, item store.ManagedPlatform, accepted store.ManagedPlatformOperation, request NeonRuntimeRequest, deprovision, useBinding bool) (*managedplatform.DurableNeonRuntime, managedplatform.NeonLifecycleRequest, backup.ObjectStore, error) {
	zones, err := r.Cluster.neonAvailabilityZones(ctx, item.Spec)
	if err != nil {
		return nil, managedplatform.NeonLifecycleRequest{}, nil, err
	}
	accepted.Lease = recovery.Lease
	if deprovision {
		accepted.Kind = "delete"
	}
	claimMode := ""
	if deprovision && useBinding {
		claimMode = "replacement"
	} else if deprovision {
		claimMode = "prior"
	}
	adapter := neonRecoveryLifecycle{runtime: r, recovery: recovery, platform: accepted, claimMode: claimMode}
	durable, lifecycle, _, err := prepareNeonLifecycleWithAdapter(ctx, NeonRuntimeRequest{Operation: accepted, Render: request.Render, SecretSnapshots: request.SecretSnapshots, ProxyEndpoint: request.ProxyEndpoint}, adapter, nil, r.EncryptionKey, zones)
	if err != nil {
		return nil, lifecycle, nil, err
	}
	if !deprovision {
		durable.SetComputeConfigResolver(neonControllerComputeResolver(r.Store, item.ID, item.Revision, item.Spec.Neon.Pageservers))
	}
	if useBinding {
		var binding store.NeonRecoveryBinding
		var bindingErr error
		if recovery.Kind == "restore" {
			binding, bindingErr = r.Store.NeonRecoveryBindingForRecovery(ctx, recovery)
		} else {
			binding, bindingErr = r.Store.NeonRecoveryBindingForTarget(ctx, item.ID, item.Revision)
		}
		if bindingErr == nil {
			lifecycle.TenantID, lifecycle.TimelineID = binding.TenantID, binding.TimelineID
			lifecycle.RecoveryTenantGeneration, lifecycle.RecoveryTimelineGeneration = binding.TenantGeneration, binding.TimelineGeneration
			safekeepers := make([]string, item.Spec.Neon.Safekeepers)
			for i := range safekeepers {
				safekeepers[i] = fmt.Sprintf("neon-safekeeper-%d.%s.svc:5454", i, "managed-platform-"+item.ID)
			}
			for name, raw := range lifecycle.ComputeConfig {
				lifecycle.ComputeConfig[name], err = bindNeonComputeConfig(raw, binding.TenantID, binding.TimelineID, safekeepers, name)
				if err != nil {
					return nil, lifecycle, nil, err
				}
				if !deprovision {
					lifecycle.ComputeConfig[name], err = managedplatform.BindNeonTenantAuthentication(lifecycle.ComputeConfig[name], r.EncryptionKey, item.ID, binding.TenantID)
					if err != nil {
						return nil, lifecycle, nil, err
					}
				}
			}
		} else if !errors.Is(bindingErr, pgx.ErrNoRows) {
			return nil, lifecycle, nil, bindingErr
		}
	}
	objects, err := r.objectStore(item, request)
	if err != nil {
		return nil, lifecycle, nil, err
	}
	return durable, lifecycle, objects, nil
}

func (r *NeonRecoveryRuntime) objectStore(item store.ManagedPlatform, request NeonRuntimeRequest) (backup.ObjectStore, error) {
	ref := item.Spec.Secrets["object-storage"]
	secret := request.SecretSnapshots[ref.Name+"-r"+strconv.FormatInt(ref.Revision, 10)]
	if len(secret["access-key-id"]) == 0 || len(secret["secret-access-key"]) == 0 {
		return nil, fmt.Errorf("accepted Neon object-storage secret is incomplete")
	}
	d := backup.Destination{Project: item.Project, Environment: item.Environment, Endpoint: item.Spec.Neon.ObjectStorageURL, Region: item.Spec.Neon.ObjectStorageRegion, Bucket: item.Spec.Neon.ObjectStorageBucket}
	return backup.NewS3(d, backup.Credentials{AccessKeyID: string(secret["access-key-id"]), SecretAccessKey: string(secret["secret-access-key"]), SessionToken: string(secret["session-token"])}), nil
}

func (r *NeonRecoveryRuntime) ResolveSource(ctx context.Context, op platformbackup.Operation) (platformbackup.Manifest, error) {
	item, plan, _, _, err := r.contract(ctx, op.SourcePlatformID, op.ExpectedSourceRevision)
	if err != nil {
		return platformbackup.Manifest{}, err
	}
	claims, err := r.Store.ManagedPlatformRecoveryClaims(ctx, item.ID, item.Revision)
	if err != nil {
		return platformbackup.Manifest{}, err
	}
	ns, err := r.Cluster.kube.CoreV1().Namespaces().Get(ctx, plan.Namespace, metav1.GetOptions{})
	if err != nil || ns.Labels["hakopod.io/managed-platform-id"] != item.ID || claims["namespace."+plan.Namespace].ResourceID != string(ns.UID) {
		return platformbackup.Manifest{}, fmt.Errorf("source Neon namespace ownership is invalid")
	}
	destination, err := r.Store.BackupDestination(ctx, op.DestinationID)
	if err != nil {
		return platformbackup.Manifest{}, err
	}
	m := platformbackup.Manifest{SchemaVersion: platformbackup.SchemaVersion, Format: platformbackup.NeonFormat, PlatformID: item.ID, PlatformRevision: item.Revision, PlatformSpec: json.RawMessage(store.JSON(item.Spec)), Release: item.Spec.Version, Images: map[string]string{}, SourceNamespace: ns.Name, SourceNamespaceUID: string(ns.UID), Consistency: "computes and proxy stopped; safekeepers checkpointed; the attached pageserver uploaded through the committed LSN", DestinationID: op.DestinationID, EncryptionRecipient: destination.EncryptionRecipient}
	for _, c := range plan.Components {
		m.Images[c.Name] = c.Image
	}
	return m, nil
}

func (r *NeonRecoveryRuntime) ResolveEmptyTarget(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) error {
	item, plan, accepted, req, err := r.preflightRestoreTarget(ctx, op, m)
	if err != nil {
		return err
	}
	prefix := strings.Trim(item.Spec.Neon.ObjectStoragePrefix, "/") + "/recovery/" + op.ID + "/"
	m.ManifestSHA256 = m.Digest()
	binding, err := r.Store.BindNeonRecoveryTarget(ctx, op, m, prefix)
	if err != nil {
		return err
	}
	if err = r.scaleProxy(ctx, op, item, plan.Namespace, 0); err != nil {
		return err
	}
	_, _, objects, err := r.durable(ctx, op, item, accepted, req, false, true)
	_ = binding
	if err != nil {
		return err
	}
	defer objects.Close()
	keys, next, err := objects.ListPrefix(ctx, prefix, "")
	if err != nil {
		return err
	}
	if len(keys) != 0 || next != "" {
		return fmt.Errorf("recovery requires an empty Neon remote-storage target")
	}
	claims, err := r.Store.EffectiveNeonRecoveryClaims(ctx, op)
	if err != nil {
		return err
	}
	for _, claim := range claims {
		providerClaim, claimErr := neonProviderRecoveryClaim(claim)
		if claimErr != nil {
			return claimErr
		}
		if !providerClaim {
			continue
		}
		externalKey, keyErr := managedplatform.NeonRecoveryExternalKey(claim.Component, claim.ResourceID, m.Neon.TenantID, m.Neon.TimelineID)
		if keyErr != nil {
			return keyErr
		}
		token := recoveryTransitionToken(op.ID, claim.Component, claim.ImmutableGeneration, 0)
		if _, err = r.Store.PlanNeonRecoveryResource(ctx, op, binding, claim, externalKey, token); err != nil {
			return err
		}
	}
	deprovision, oldLifecycle, oldObjects, err := r.durable(ctx, op, item, accepted, req, true, false)
	if oldObjects != nil {
		defer oldObjects.Close()
	}
	if err != nil {
		return err
	}
	if err = deprovision.Deprovision(ctx, oldLifecycle); err != nil {
		return fmt.Errorf("deprovision target Neon bootstrap identities: %w", err)
	}
	if err = r.scaleServingWorkloads(ctx, op, item, plan.Namespace, 0, false); err != nil {
		return err
	}
	if err = r.scaleStorage(ctx, op, item, 0); err != nil {
		return err
	}
	if err = r.rebindStoragePrefix(ctx, op, item, plan, req, binding); err != nil {
		return err
	}
	return nil
}

func neonProviderRecoveryClaim(claim store.PlatformResourceClaim) (bool, error) {
	if claim.Component != "tenant" && claim.Component != "timeline" && !strings.HasPrefix(claim.Component, "compute-") {
		return false, nil
	}
	if claim.Kind != "neon_tenant" && claim.Kind != "neon_timeline" && claim.Kind != "runtime_component" {
		return false, fmt.Errorf("Neon provider recovery claim kind changed")
	}
	return true, nil
}

func (r *NeonRecoveryRuntime) PreflightRestoreTarget(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) error {
	_, _, _, _, err := r.preflightRestoreTarget(ctx, op, m)
	return err
}

func (r *NeonRecoveryRuntime) preflightRestoreTarget(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) (store.ManagedPlatform, managedplatform.Plan, store.ManagedPlatformOperation, NeonRuntimeRequest, error) {
	if m.Neon == nil {
		return store.ManagedPlatform{}, managedplatform.Plan{}, store.ManagedPlatformOperation{}, NeonRuntimeRequest{}, platformbackup.ErrInvalid
	}
	item, plan, accepted, req, err := r.contract(ctx, op.TargetPlatformID, op.ExpectedTargetRevision)
	if err != nil {
		return item, plan, accepted, req, err
	}
	if item.ID == m.PlatformID || item.Spec.Version != m.Release {
		return item, plan, accepted, req, fmt.Errorf("restore requires a separate compatible Neon target")
	}
	for name, image := range m.Images {
		found := false
		for _, c := range plan.Components {
			if c.Name == name && c.Image == image {
				found = true
			}
		}
		if !found {
			return item, plan, accepted, req, fmt.Errorf("target Neon image inventory differs from the artifact")
		}
	}
	prefix := strings.Trim(item.Spec.Neon.ObjectStoragePrefix, "/") + "/recovery/" + op.ID + "/"
	m.ManifestSHA256 = m.Digest()
	binding, bindingErr := r.Store.NeonRecoveryBindingForRecovery(ctx, op)
	if errors.Is(bindingErr, pgx.ErrNoRows) {
		binding, bindingErr = r.Store.NeonRecoveryBindingForTarget(ctx, item.ID, item.Revision)
	}
	if bindingErr == nil && (binding.OperationID != op.ID || binding.ArtifactID != op.ArtifactID || binding.ManifestSHA256 != m.ManifestSHA256 || binding.StagingPrefix != prefix) {
		return item, plan, accepted, req, fmt.Errorf("%w: this Neon target revision already has an immutable recovery binding; create and review a fresh target revision", store.ErrConflict)
	}
	if bindingErr != nil && !errors.Is(bindingErr, pgx.ErrNoRows) {
		return item, plan, accepted, req, bindingErr
	}
	if err = r.fence(ctx, op); err != nil {
		return item, plan, accepted, req, err
	}
	objects, objectErr := r.objectStore(item, req)
	if objectErr != nil {
		return item, plan, accepted, req, objectErr
	}
	defer objects.Close()
	keys, next, listErr := objects.ListPrefix(ctx, prefix, "")
	if listErr != nil {
		return item, plan, accepted, req, listErr
	}
	if len(keys) != 0 || next != "" {
		return item, plan, accepted, req, fmt.Errorf("recovery requires an empty Neon remote-storage target")
	}
	return item, plan, accepted, req, nil
}

func (r *NeonRecoveryRuntime) PauseWrites(ctx context.Context, op platformbackup.Operation) error {
	item, plan, _, _, err := r.contract(ctx, op.SourcePlatformID, op.ExpectedSourceRevision)
	if err != nil {
		return err
	}
	return r.scaleServing(ctx, op, item, plan.Namespace, 0)
}
func (r *NeonRecoveryRuntime) DrainWrites(ctx context.Context, op platformbackup.Operation) error {
	item, plan, _, _, err := r.contract(ctx, op.SourcePlatformID, op.ExpectedSourceRevision)
	if err != nil {
		return err
	}
	return r.waitServingPods(ctx, item.ID, plan.Namespace, 0)
}

func tempCaptured(name string, data []byte) (platformbackup.CapturedPart, error) {
	f, err := os.CreateTemp("", "hakopod-neon-recovery-*")
	if err != nil {
		return platformbackup.CapturedPart{}, err
	}
	fail := func(e error) (platformbackup.CapturedPart, error) {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return platformbackup.CapturedPart{}, e
	}
	if err = f.Chmod(0600); err != nil {
		return fail(err)
	}
	if _, err = f.Write(data); err != nil {
		return fail(err)
	}
	if err = f.Sync(); err != nil {
		return fail(err)
	}
	sum := sha256.Sum256(data)
	path := f.Name()
	_ = f.Close()
	return platformbackup.CapturedPart{Part: platformbackup.Part{Name: name, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))}, Open: func() (io.ReadCloser, error) { return os.Open(path) }, Cleanup: func() error { return os.Remove(path) }}, nil
}

func (r *NeonRecoveryRuntime) Capture(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) ([]platformbackup.CapturedPart, map[string]string, error) {
	item, _, accepted, req, err := r.contract(ctx, op.SourcePlatformID, op.ExpectedSourceRevision)
	if err != nil {
		return nil, nil, err
	}
	durable, lifecycle, objects, err := r.durable(ctx, op, item, accepted, req, false, true)
	if err != nil {
		return nil, nil, err
	}
	defer objects.Close()
	fence, err := durable.CheckpointOwnedRecovery(ctx, lifecycle.TenantID, lifecycle.TimelineID)
	if err != nil {
		return nil, nil, err
	}
	if err = r.scaleStorage(ctx, op, item, 0); err != nil {
		return nil, nil, err
	}
	prefix := strings.Trim(item.Spec.Neon.ObjectStoragePrefix, "/") + "/"
	if binding, bindingErr := r.Store.NeonRecoveryBindingForTarget(ctx, item.ID, item.Revision); bindingErr == nil {
		prefix = strings.TrimSuffix(binding.StagingPrefix, "/") + "/"
	} else if !errors.Is(bindingErr, pgx.ErrNoRows) {
		return nil, nil, bindingErr
	}
	remote, inventory, err := backup.CaptureNeonRemotePrefix(ctx, objects, prefix, platformbackup.MaxArchiveBytes)
	if err != nil {
		return nil, nil, err
	}
	tenantJSON, _ := json.Marshal(map[string]any{"tenant_id": fence.TenantID, "generation": fence.TenantGeneration})
	timelineJSON, _ := json.Marshal(map[string]any{"timeline_id": fence.TimelineID, "generation": fence.TimelineGeneration, "commit_lsn": fence.CommitLSN, "pageservers": fence.Pageservers})
	tenant, err := tempCaptured("tenant.json", tenantJSON)
	if err != nil {
		_ = remote.Close()
		_ = os.Remove(remote.Name())
		return nil, nil, err
	}
	timeline, err := tempCaptured("timeline.json", timelineJSON)
	if err != nil {
		_ = tenant.Cleanup()
		_ = remote.Close()
		_ = os.Remove(remote.Name())
		return nil, nil, err
	}
	remotePath := remote.Name()
	_ = remote.Close()
	remoteBytes, remoteDigest, err := recoveryFileDigest(remotePath)
	if err != nil {
		_ = tenant.Cleanup()
		_ = timeline.Cleanup()
		_ = os.Remove(remotePath)
		return nil, nil, err
	}
	remotePart := platformbackup.CapturedPart{Part: platformbackup.Part{Name: "remote-storage.tar", SHA256: remoteDigest, Bytes: remoteBytes}, Open: func() (io.ReadCloser, error) { return os.Open(remotePath) }, Cleanup: func() error { return os.Remove(remotePath) }}
	identity := platformbackup.NeonIdentity{TenantID: fence.TenantID, TimelineID: fence.TimelineID, TenantGeneration: fence.TenantGeneration, TimelineGeneration: fence.TimelineGeneration, CommitLSN: fence.CommitLSN, PageserverRemoteConsistentLSNs: fence.Pageservers, SourceObjectPrefix: prefix, ObjectInventorySHA256: inventory.SHA256, ObjectCount: inventory.Count, ObjectBytes: inventory.Bytes}
	if err = r.Store.SaveNeonRecoveryCapture(ctx, op, identity); err != nil {
		_ = tenant.Cleanup()
		_ = timeline.Cleanup()
		_ = remotePart.Cleanup()
		return nil, nil, err
	}
	evidence := map[string]string{"tenant_identity": tenant.SHA256, "tenant_generation": tenant.SHA256, "timeline_identity": timeline.SHA256, "timeline_generation": timeline.SHA256, "remote_storage": inventory.SHA256}
	return []platformbackup.CapturedPart{tenant, timeline, remotePart}, evidence, nil
}

func (r *NeonRecoveryRuntime) FinalizeCapture(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) (platformbackup.Manifest, error) {
	identity, err := r.Store.NeonRecoveryCapture(ctx, op)
	if err != nil {
		return m, err
	}
	m.Neon = &identity
	return m, nil
}

func (r *NeonRecoveryRuntime) ReobserveSourceClaims(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) error {
	current, err := r.ResolveSource(ctx, op)
	if err != nil {
		return err
	}
	if current.SourceNamespaceUID != m.SourceNamespaceUID || !bytes.Equal(store.JSON(current.Images), store.JSON(m.Images)) {
		return fmt.Errorf("source Neon ownership changed")
	}
	return nil
}
func (r *NeonRecoveryRuntime) ResumeSource(ctx context.Context, op platformbackup.Operation) error {
	item, plan, _, _, err := r.contract(ctx, op.SourcePlatformID, op.ExpectedSourceRevision)
	if err != nil {
		return err
	}
	if err = r.scaleStorage(ctx, op, item, -1); err != nil {
		return err
	}
	return r.scaleServing(ctx, op, item, plan.Namespace, -1)
}

func (r *NeonRecoveryRuntime) RestorePart(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest, part platformbackup.Part, input io.Reader) error {
	if m.Neon == nil {
		return platformbackup.ErrInvalid
	}
	binding, err := r.Store.NeonRecoveryBindingForRecovery(ctx, op)
	if err != nil {
		return err
	}
	if binding.ArtifactID != op.ArtifactID || binding.ManifestSHA256 != m.ManifestSHA256 {
		return store.ErrConflict
	}
	if part.Name == "tenant.json" || part.Name == "timeline.json" {
		data, readErr := io.ReadAll(io.LimitReader(input, part.Bytes+1))
		if readErr != nil || int64(len(data)) != part.Bytes {
			return platformbackup.ErrInvalid
		}
		if part.Name == "tenant.json" {
			var value struct {
				TenantID   string `json:"tenant_id"`
				Generation int64  `json:"generation"`
			}
			if json.Unmarshal(data, &value) != nil || value.TenantID != m.Neon.TenantID || value.Generation != m.Neon.TenantGeneration {
				return platformbackup.ErrInvalid
			}
		} else {
			var value struct {
				TimelineID  string            `json:"timeline_id"`
				Generation  int64             `json:"generation"`
				CommitLSN   string            `json:"commit_lsn"`
				Pageservers map[string]string `json:"pageservers"`
			}
			if json.Unmarshal(data, &value) != nil || value.TimelineID != m.Neon.TimelineID || value.Generation != m.Neon.TimelineGeneration || value.CommitLSN != m.Neon.CommitLSN || !bytes.Equal(store.JSON(value.Pageservers), store.JSON(m.Neon.PageserverRemoteConsistentLSNs)) {
				return platformbackup.ErrInvalid
			}
		}
		return nil
	}
	if part.Name != "remote-storage.tar" {
		return platformbackup.ErrInvalid
	}
	item, _, accepted, req, err := r.contract(ctx, op.TargetPlatformID, op.ExpectedTargetRevision)
	if err != nil {
		return err
	}
	_, _, objects, err := r.durable(ctx, op, item, accepted, req, false, true)
	if err != nil {
		return err
	}
	defer objects.Close()
	return backup.RestoreNeonRemotePrefix(ctx, objects, binding.StagingPrefix, input, backup.NeonRemoteInventory{Count: m.Neon.ObjectCount, Bytes: m.Neon.ObjectBytes, SHA256: m.Neon.ObjectInventorySHA256}, platformbackup.MaxArchiveBytes)
}

func recoveryFileDigest(name string) (int64, string, error) {
	f, err := os.Open(name)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, platformbackup.MaxArchiveBytes+1))
	if err != nil || n > platformbackup.MaxArchiveBytes {
		return 0, "", platformbackup.ErrInvalid
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
func (r *NeonRecoveryRuntime) VerifyRestoredContent(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) error {
	binding, err := r.Store.NeonRecoveryBindingForRecovery(ctx, op)
	if err != nil {
		return err
	}
	item, _, accepted, req, err := r.contract(ctx, op.TargetPlatformID, op.ExpectedTargetRevision)
	if err != nil {
		return err
	}
	_, _, objects, err := r.durable(ctx, op, item, accepted, req, false, true)
	if err != nil {
		return err
	}
	defer objects.Close()
	file, inventory, err := backup.CaptureNeonRemotePrefix(ctx, objects, binding.StagingPrefix, platformbackup.MaxArchiveBytes)
	if file != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	if err != nil {
		return err
	}
	if inventory.Count != m.Neon.ObjectCount || inventory.Bytes != m.Neon.ObjectBytes || inventory.SHA256 != m.Neon.ObjectInventorySHA256 {
		return fmt.Errorf("restored Neon content verification failed")
	}
	return nil
}
func (r *NeonRecoveryRuntime) VerifyRestoredRuntime(ctx context.Context, op platformbackup.Operation, m platformbackup.Manifest) error {
	if m.Neon == nil {
		return platformbackup.ErrInvalid
	}
	item, plan, accepted, request, err := r.contract(ctx, op.TargetPlatformID, op.ExpectedTargetRevision)
	if err != nil {
		return err
	}
	if err = r.scaleStorage(ctx, op, item, -1); err != nil {
		return err
	}
	// The target was fenced and all compute pods removed before importing
	// storage. Start empty controls before configuring the recovered tenant;
	// each PostgreSQL child receives the recovered tenant's storage token.
	if err = r.scaleServingWorkloads(ctx, op, item, plan.Namespace, -1, false); err != nil {
		return err
	}
	durable, lifecycle, objects, err := r.durable(ctx, op, item, accepted, request, false, true)
	if objects != nil {
		defer objects.Close()
	}
	if err != nil {
		return err
	}
	state, err := durable.Provision(ctx, lifecycle)
	if err != nil {
		return fmt.Errorf("provision recovered Neon identities: %w", err)
	}
	if !state.Complete || state.TenantID != m.Neon.TenantID || state.TimelineID != m.Neon.TimelineID {
		return fmt.Errorf("recovered Neon control-plane identity did not verify")
	}
	fence, err := durable.CheckpointOwnedRecovery(ctx, m.Neon.TenantID, m.Neon.TimelineID)
	if err != nil {
		return fmt.Errorf("verify recovered Neon storage boundary: %w", err)
	}
	checkpointLSN, checkpointErr := parseRecoveryLSNOutput(fence.CommitLSN)
	requiredLSN, requiredErr := parseRecoveryLSNOutput(m.Neon.CommitLSN)
	// Starting the restored compute can append WAL. Its verified storage must
	// contain the artifact's boundary, even when the controller moves it.
	if fence.TenantID != m.Neon.TenantID || fence.TimelineID != m.Neon.TimelineID || fence.TenantGeneration != m.Neon.TenantGeneration || fence.TimelineGeneration != m.Neon.TimelineGeneration || checkpointErr != nil || requiredErr != nil || checkpointLSN < requiredLSN || len(fence.Pageservers) != 1 {
		return fmt.Errorf("recovered Neon storage identity or LSN differs from the artifact")
	}
	claims, err := r.Store.EffectiveNeonRecoveryClaims(ctx, op)
	if err != nil {
		return err
	}
	for _, component := range []string{"tenant", "timeline"} {
		claim, ok := claims[component]
		if !ok {
			return fmt.Errorf("recovered Neon %s claim is unavailable", component)
		}
		external, keyErr := managedplatform.NeonRecoveryExternalKey(component, claim.ResourceID, m.Neon.TenantID, m.Neon.TimelineID)
		if keyErr != nil {
			return keyErr
		}
		if component == "tenant" && (external != m.Neon.TenantID || claim.ImmutableGeneration != m.Neon.TenantGeneration) || component == "timeline" && (external != m.Neon.TenantID+"/"+m.Neon.TimelineID || claim.ImmutableGeneration != m.Neon.TimelineGeneration) {
			return fmt.Errorf("recovered Neon %s generation did not verify", component)
		}
	}
	binding, err := r.Store.NeonRecoveryBindingForRecovery(ctx, op)
	if err != nil {
		return err
	}
	endpoint, err := r.Store.NeonProxyEndpoint(ctx, item.ID)
	if err != nil {
		return err
	}
	compute, err := r.computePod(ctx, op, item, plan.Namespace, request)
	if err != nil {
		return err
	}
	var sqlProbe strings.Builder
	if err = r.execCompute(ctx, op, item, request, compute, []string{"psql", "-XAtw", "-v", "ON_ERROR_STOP=1", "-h", "127.0.0.1", "-p", "55433", "-U", "cloud_admin", "-d", "postgres", "-c", "BEGIN READ ONLY; SELECT pg_current_wal_lsn()::text; COMMIT"}, &sqlProbe); err != nil {
		return fmt.Errorf("recovered Neon SQL read boundary did not verify")
	}
	currentLSN, parseErr := parseRecoveryLSNOutput(sqlProbe.String())
	artifactLSN, artifactErr := parseRecoveryLSNOutput(m.Neon.CommitLSN)
	if parseErr != nil || artifactErr != nil || currentLSN < artifactLSN {
		return fmt.Errorf("recovered Neon SQL WAL boundary did not reach the artifact commit LSN")
	}
	if err = r.Store.RebindNeonRecoveryProxyEndpoint(ctx, op, binding, endpoint, m.Neon.TenantID, m.Neon.TimelineID, "compute-0"); err != nil {
		return err
	}
	if err = r.scaleServing(ctx, op, item, plan.Namespace, -1); err != nil {
		return err
	}
	return r.waitServingPods(ctx, item.ID, plan.Namespace, item.Spec.Neon.ComputeReplicas+1)
}

func (r *NeonRecoveryRuntime) computePod(ctx context.Context, op platformbackup.Operation, item store.ManagedPlatform, namespace string, request NeonRuntimeRequest) (*corev1.Pod, error) {
	selector := labels.Set{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": item.ID, "app.kubernetes.io/component": "compute-0"}.AsSelector().String()
	pods, err := r.Cluster.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: 2})
	if err != nil || pods.Continue != "" || len(pods.Items) != 1 {
		return nil, fmt.Errorf("owned Neon compute pod is unavailable")
	}
	pod := pods.Items[0].DeepCopy()
	ns, err := r.Cluster.kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil || ns.DeletionTimestamp != nil || ns.Labels["hakopod.io/managed-platform-id"] != item.ID {
		return nil, fmt.Errorf("owned Neon compute namespace changed")
	}
	claims, err := r.Store.ManagedPlatformRecoveryClaims(ctx, item.ID, item.Revision)
	if err != nil || claims["namespace."+namespace].ResourceID != string(ns.UID) {
		return nil, fmt.Errorf("owned Neon compute namespace claim changed")
	}
	if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].Controller == nil || !*pod.OwnerReferences[0].Controller || pod.OwnerReferences[0].Kind != "StatefulSet" {
		return nil, fmt.Errorf("owned Neon compute controller changed")
	}
	set, err := r.Cluster.kube.AppsV1().StatefulSets(namespace).Get(ctx, pod.OwnerReferences[0].Name, metav1.GetOptions{})
	claim := claims["statefulset."+pod.OwnerReferences[0].Name]
	if err != nil || set.UID != pod.OwnerReferences[0].UID || len(set.OwnerReferences) != 1 || set.OwnerReferences[0].UID != ns.UID || claim.ResourceID != string(set.UID) {
		return nil, fmt.Errorf("owned Neon compute controller changed")
	}
	expectedGeneration, err := r.Store.PlatformRecoveryDeploymentGeneration(ctx, op, set.Name, claim.ImmutableGeneration)
	if err != nil || set.Generation != expectedGeneration {
		return nil, fmt.Errorf("owned Neon compute generation changed")
	}
	if err = validateNeonComputeContainers(pod, request.Render.Images); err != nil {
		return nil, err
	}
	return pod, nil
}

func validateNeonComputeContainers(pod *corev1.Pod, images map[string]string) error {
	if pod == nil || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || len(pod.Spec.Containers) != 2 || len(pod.Status.ContainerStatuses) != 2 {
		return fmt.Errorf("owned Neon compute pod identity changed")
	}
	expectedImages := map[string]string{"compute": images["compute"], "compute-tls": images["compute-tls"]}
	seen := map[string]bool{}
	for _, container := range pod.Spec.Containers {
		if expectedImages[container.Name] == "" || container.Image != expectedImages[container.Name] || seen[container.Name] {
			return fmt.Errorf("owned Neon compute pod image changed")
		}
		seen[container.Name] = true
	}
	for _, status := range pod.Status.ContainerStatuses {
		expected := expectedImages[status.Name]
		digestIndex := strings.LastIndex(expected, "@sha256:")
		if !status.Ready || digestIndex < 0 || !strings.HasSuffix(status.ImageID, expected[digestIndex+1:]) {
			return fmt.Errorf("owned Neon compute runtime image changed")
		}
	}
	return nil
}

func sameNeonExecPod(selected, current *corev1.Pod) bool {
	return selected != nil && current != nil && current.Namespace == selected.Namespace && current.Name == selected.Name && current.UID == selected.UID && current.ResourceVersion == selected.ResourceVersion
}

func (r *NeonRecoveryRuntime) execCompute(ctx context.Context, op platformbackup.Operation, item store.ManagedPlatform, request NeonRuntimeRequest, pod *corev1.Pod, command []string, stdout io.Writer) error {
	if r.Cluster.execConfig == nil || len(command) == 0 {
		return fmt.Errorf("Neon recovery execution transport is unavailable")
	}
	current, err := r.computePod(ctx, op, item, pod.Namespace, request)
	if err != nil || !sameNeonExecPod(pod, current) {
		return fmt.Errorf("owned Neon compute pod identity changed before execution")
	}
	if err = r.fence(ctx, op); err != nil {
		return err
	}
	step, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	requestURL := r.Cluster.restClient().Post().Resource("pods").Namespace(current.Namespace).Name(current.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: "compute", Command: command, Stdout: stdout != nil, Stderr: true}, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(r.Cluster.execConfig, http.MethodPost, requestURL)
	if err != nil {
		return err
	}
	limited := &recoveryLimitWriter{Writer: stdout, Remaining: 4 << 10}
	if err = executor.StreamWithContext(step, remotecommand.StreamOptions{Stdout: limited, Stderr: io.Discard}); err != nil {
		return fmt.Errorf("Neon recovery SQL verification failed")
	}
	return nil
}

func (r *NeonRecoveryRuntime) CleanupRestore(ctx context.Context, op platformbackup.Operation) error {
	item, _, accepted, request, err := r.contract(ctx, op.TargetPlatformID, op.ExpectedTargetRevision)
	if err != nil {
		return err
	}
	if err = r.fence(ctx, op); err != nil {
		return err
	}
	binding, err := r.Store.NeonRecoveryBindingForRecovery(ctx, op)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if binding.OperationID != op.ID {
		return store.ErrConflict
	}
	namespace := "managed-platform-" + item.ID
	if err = r.scaleProxy(ctx, op, item, namespace, 0); err != nil {
		return err
	}
	if err = r.scaleStorageRoles(ctx, op, item, 0, 1); err != nil {
		return err
	}
	if err = r.scaleServingWorkloads(ctx, op, item, namespace, 1, false); err != nil {
		_ = r.scaleStorage(ctx, op, item, 0)
		return err
	}
	defer func() {
		_ = r.scaleServingWorkloads(ctx, op, item, namespace, 0, false)
		_ = r.scaleStorage(ctx, op, item, 0)
	}()
	resources, err := r.Store.NeonRecoveryResources(ctx, op)
	if err != nil {
		return err
	}
	priorRuntime, priorLifecycle, priorObjects, err := r.durable(ctx, op, item, accepted, request, false, false)
	if priorObjects != nil {
		defer priorObjects.Close()
	}
	if err != nil {
		return err
	}
	inspectionRuntime, inspectionLifecycle, inspectionObjects, err := r.durable(ctx, op, item, accepted, request, false, true)
	if inspectionObjects != nil {
		defer inspectionObjects.Close()
	}
	if err != nil {
		return err
	}
	for _, resource := range resources {
		if resource.Phase == "planned" {
			claim := managedplatform.DurableResourceClaim{PlatformID: resource.TargetPlatformID, PlatformRevision: resource.TargetRevision, Component: resource.Component, Kind: resource.Kind, ResourceID: resource.PriorResourceID, ImmutableGeneration: resource.PriorGeneration, OwnerOperationID: resource.PriorOwnerOperationID}
			exists, inspectErr := priorRuntime.InspectOwnedRecoveryClaim(ctx, priorLifecycle, claim)
			if inspectErr != nil {
				return inspectErr
			}
			if exists {
				if _, err = r.Store.CompleteNeonRecoveryUntouchedResource(ctx, op, resource); err != nil {
					return err
				}
				continue
			}
			released, releaseErr := r.Store.MarkNeonRecoveryPriorReleased(ctx, op, resource.Component, resource.PriorResourceID, resource.PriorGeneration, resource.TransitionToken)
			if releaseErr != nil {
				return releaseErr
			}
			if _, err = r.Store.CompleteNeonRecoveryEmptyReservation(ctx, op, released); err != nil {
				return err
			}
			continue
		}
		if resource.Phase == "prior_released" {
			if _, err = r.Store.CompleteNeonRecoveryEmptyReservation(ctx, op, resource); err != nil {
				return err
			}
			continue
		}
		if resource.Phase != "reserved" {
			continue
		}
		intent := managedplatform.DurableResourceIntent{ID: resource.TransitionToken, PlatformID: resource.TargetPlatformID, PlatformRevision: resource.TargetRevision, Component: resource.Component, Kind: resource.Kind, ExternalKey: resource.ReplacementExternalKey, OwnerOperationID: accepted.ID}
		providerID, generation, exists, inspectErr := inspectionRuntime.InspectOwnedRecoveryReservation(ctx, inspectionLifecycle, intent)
		if inspectErr != nil {
			return inspectErr
		}
		if exists {
			if _, err = r.Store.ConfirmNeonRecoveryCleanupReservation(ctx, op, resource, providerID, generation); err != nil {
				return err
			}
		} else if _, err = r.Store.CompleteNeonRecoveryEmptyReservation(ctx, op, resource); err != nil {
			return err
		}
	}
	resources, err = r.Store.NeonRecoveryResources(ctx, op)
	if err != nil {
		return err
	}
	deprovisionReplacement := false
	for _, resource := range resources {
		if resource.Phase == "confirmed" || resource.Phase == "replacement_released" {
			deprovisionReplacement = true
		}
	}
	var objects backup.ObjectStore
	if deprovisionReplacement {
		durable, lifecycle, replacementObjects, durableErr := r.durable(ctx, op, item, accepted, request, true, true)
		objects = replacementObjects
		if durableErr != nil {
			if objects != nil {
				objects.Close()
			}
			return durableErr
		}
		if err = durable.Deprovision(ctx, lifecycle); err != nil {
			if objects != nil {
				objects.Close()
			}
			return err
		}
	}
	if err = r.scaleServingWorkloads(ctx, op, item, namespace, 0, false); err != nil {
		return err
	}
	if err = r.scaleStorage(ctx, op, item, 0); err != nil {
		return err
	}
	if objects == nil {
		_, _, objects, err = r.durable(ctx, op, item, accepted, request, false, true)
	}
	if objects != nil {
		defer objects.Close()
	}
	if err != nil {
		return err
	}
	// A full final batch reports that objects may remain. Allow one bounded
	// empty-list pass to prove that the operation-scoped prefix is gone.
	maxCalls := (backup.MaxNeonRecoveryObjects+backup.MaxPrefixDeletePerCall-1)/backup.MaxPrefixDeletePerCall + 1
	for call := 0; call < maxCalls; call++ {
		if err = r.fence(ctx, op); err != nil {
			return err
		}
		remaining, deleteErr := objects.DeletePrefix(ctx, binding.StagingPrefix)
		if deleteErr != nil {
			return deleteErr
		}
		if !remaining {
			return r.Store.MarkPlatformRecoveryTargetIsolated(ctx, op, "Restore cleanup completed; this target remains isolated and requires a newly reviewed revision before use.")
		}
	}
	return fmt.Errorf("Neon recovery cleanup exceeded the bounded remote object inventory")
}

func (r *NeonRecoveryRuntime) rebindStoragePrefix(ctx context.Context, op platformbackup.Operation, item store.ManagedPlatform, plan managedplatform.Plan, request NeonRuntimeRequest, binding store.NeonRecoveryBinding) error {
	ns, err := r.Cluster.kube.CoreV1().Namespaces().Get(ctx, plan.Namespace, metav1.GetOptions{})
	if err != nil || ns.DeletionTimestamp != nil || ns.Labels["hakopod.io/managed-platform-id"] != item.ID {
		return fmt.Errorf("target Neon namespace ownership is invalid")
	}
	claims, err := r.Store.ManagedPlatformRecoveryClaims(ctx, item.ID, item.Revision)
	if err != nil {
		return err
	}
	if claims["namespace."+ns.Name].ResourceID != string(ns.UID) {
		return fmt.Errorf("target Neon namespace ownership changed")
	}
	request.Render.NamespaceUID = ns.UID
	request.Render.RecoveryStoragePrefix = strings.TrimSuffix(binding.StagingPrefix, "/")
	manifests, err := managedplatform.RenderNeon(request.Render)
	if err != nil {
		return err
	}
	desiredSets := map[string]*appsv1.StatefulSet{}
	desiredConfigs := map[string]*corev1.ConfigMap{}
	for _, object := range manifests.Objects {
		switch value := object.(type) {
		case *appsv1.StatefulSet:
			if strings.HasPrefix(value.Name, "neon-pageserver-") || strings.HasPrefix(value.Name, "neon-safekeeper-") {
				desiredSets[value.Name] = value.DeepCopy()
			}
		case *corev1.ConfigMap:
			if strings.HasPrefix(value.Name, "neon-pageserver-") {
				desiredConfigs[value.Name] = value.DeepCopy()
			}
		}
	}
	if len(desiredSets) != item.Spec.Neon.Pageservers+item.Spec.Neon.Safekeepers || len(desiredConfigs) != item.Spec.Neon.Pageservers {
		return fmt.Errorf("rendered Neon recovery storage inventory changed")
	}
	for name, desired := range desiredSets {
		if err = r.fence(ctx, op); err != nil {
			return err
		}
		current, getErr := r.Cluster.kube.AppsV1().StatefulSets(ns.Name).Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		claim := claims["statefulset."+name]
		if claim.ResourceID != string(current.UID) {
			return fmt.Errorf("Neon storage writer ownership changed")
		}
		desiredTemplate := desired.Spec.Template.DeepCopy()
		if strings.HasPrefix(name, "neon-pageserver-") {
			ordinal := strings.TrimPrefix(name, "neon-pageserver-")
			baseName := "neon-pageserver-" + ordinal + "-r" + strconv.FormatInt(item.Revision, 10)
			baseClaim := claims["configmap."+baseName]
			base, baseErr := r.Cluster.kube.CoreV1().ConfigMaps(ns.Name).Get(ctx, baseName, metav1.GetOptions{})
			if baseErr != nil || baseClaim.ResourceID != string(base.UID) {
				return fmt.Errorf("Neon pageserver configuration ownership changed")
			}
			config := desiredConfigs[baseName]
			if config == nil {
				return fmt.Errorf("rendered Neon pageserver configuration is missing")
			}
			config.Name = "neon-pageserver-" + ordinal + "-recovery-" + op.ID[:12]
			config.ResourceVersion = ""
			config.UID = ""
			if err = ensureNeonRecoveryConfigMap(ctx, r.Cluster.kube, ns.UID, config); err != nil {
				return err
			}
			for i := range desiredTemplate.Spec.Volumes {
				if desiredTemplate.Spec.Volumes[i].Name == "config" && desiredTemplate.Spec.Volumes[i].ConfigMap != nil {
					desiredTemplate.Spec.Volumes[i].ConfigMap.Name = config.Name
				}
			}
		}
		desiredSet := current.DeepCopy()
		desiredSet.Spec.Template = *desiredTemplate
		desiredSet.Spec.Replicas = new(int32)
		if err = applyRecoveryStatefulSetMutation(ctx, r.Store, op, item.ID, current, desiredSet, claim.ImmutableGeneration, func() error { return r.fence(ctx, op) }, func(value *appsv1.StatefulSet, options metav1.UpdateOptions) (*appsv1.StatefulSet, error) {
			return r.Cluster.kube.AppsV1().StatefulSets(ns.Name).Update(ctx, value, options)
		}); err != nil {
			return err
		}
	}
	return r.waitStoragePods(ctx, item.ID, ns.Name, 0)
}

func ensureNeonRecoveryConfigMap(ctx context.Context, kube kubernetes.Interface, namespaceUID types.UID, desired *corev1.ConfigMap) error {
	created, err := kube.CoreV1().ConfigMaps(desired.Namespace).Create(ctx, desired, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		created, err = kube.CoreV1().ConfigMaps(desired.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	}
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(created.Data, desired.Data) || len(created.OwnerReferences) != 1 || created.OwnerReferences[0].UID != namespaceUID {
		return fmt.Errorf("Neon recovery configuration collision")
	}
	return nil
}

func (r *NeonRecoveryRuntime) scaleServing(ctx context.Context, op platformbackup.Operation, item store.ManagedPlatform, namespace string, target int32) error {
	return r.scaleServingWorkloads(ctx, op, item, namespace, target, true)
}
func (r *NeonRecoveryRuntime) scaleServingWorkloads(ctx context.Context, op platformbackup.Operation, item store.ManagedPlatform, namespace string, target int32, includeProxy bool) error {
	claims, err := r.Store.ManagedPlatformRecoveryClaims(ctx, item.ID, item.Revision)
	if err != nil {
		return err
	}
	sets, err := r.Cluster.kube.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=hakopod,hakopod.io/managed-platform-id=" + item.ID, Limit: maxNeonRecoveryWorkloads})
	if err != nil {
		return err
	}
	if sets.Continue != "" {
		return fmt.Errorf("Neon workload inventory exceeds the recovery bound")
	}
	computeCount := 0
	computePods := 0
	for i := range sets.Items {
		set := sets.Items[i].DeepCopy()
		if !strings.HasPrefix(set.Name, "neon-compute-") {
			continue
		}
		computeCount++
		claim := claims["statefulset."+set.Name]
		if claim.ResourceID != string(set.UID) {
			return fmt.Errorf("Neon compute ownership changed")
		}
		desired := target
		if desired < 0 {
			desired, err = r.Store.PlatformRecoveryPriorReplicas(ctx, op, item.ID, set.Name, string(set.UID))
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
		}
		computePods += int(desired)
		if err = r.transitionStatefulSet(ctx, op, item, set, desired, claim.ImmutableGeneration); err != nil {
			return err
		}
	}
	if !includeProxy {
		return r.waitServingPods(ctx, item.ID, namespace, computePods)
	}
	if err = r.scaleProxy(ctx, op, item, namespace, target); err != nil {
		return err
	}
	if target >= 0 {
		return r.waitServingPods(ctx, item.ID, namespace, int(target)*(computeCount+1))
	}
	return nil
}

func (r *NeonRecoveryRuntime) scaleProxy(ctx context.Context, op platformbackup.Operation, item store.ManagedPlatform, namespace string, target int32) error {
	claims, err := r.Store.ManagedPlatformRecoveryClaims(ctx, item.ID, item.Revision)
	if err != nil {
		return err
	}
	deploy, err := r.Cluster.kube.AppsV1().Deployments(namespace).Get(ctx, "neon-proxy", metav1.GetOptions{})
	if err == nil {
		claim := claims["deployment."+deploy.Name]
		if claim.ResourceID != string(deploy.UID) {
			return fmt.Errorf("Neon proxy ownership changed")
		}
		desired := target
		if desired < 0 {
			desired, err = r.Store.PlatformRecoveryPriorReplicas(ctx, op, item.ID, deploy.Name, string(deploy.UID))
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
		}
		if err == nil {
			_, err = (&SupabaseRecoveryRuntime{Store: r.Store, Cluster: r.Cluster}).transitionDeployment(ctx, item, deploy, desired, claim.ImmutableGeneration)
		}
	}
	if err != nil {
		return err
	}
	if target >= 0 {
		return r.waitComponentPods(ctx, item.ID, namespace, "proxy", int(target))
	}
	return nil
}

func (r *NeonRecoveryRuntime) waitComponentPods(ctx context.Context, platformID, namespace, component string, expected int) error {
	deadline := time.Now().Add(2 * time.Minute)
	selector := labels.Set{"app.kubernetes.io/managed-by": "hakopod", "hakopod.io/managed-platform-id": platformID, "app.kubernetes.io/component": component}.AsSelector().String()
	for {
		pods, err := r.Cluster.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, Limit: maxNeonRecoveryWorkloads})
		if err != nil {
			return err
		}
		if pods.Continue != "" {
			return fmt.Errorf("Neon pod inventory exceeds the recovery bound")
		}
		count := 0
		for _, pod := range pods.Items {
			if expected == 0 || pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning && allContainersReady(pod.Status.ContainerStatuses) {
				count++
			}
		}
		if count == expected {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Neon %s pod transition timed out", component)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (r *NeonRecoveryRuntime) scaleStorage(ctx context.Context, op platformbackup.Operation, item store.ManagedPlatform, target int32) error {
	return r.scaleStorageRoles(ctx, op, item, target, target)
}

func (r *NeonRecoveryRuntime) scaleStorageRoles(ctx context.Context, op platformbackup.Operation, item store.ManagedPlatform, pageserverTarget, safekeeperTarget int32) error {
	claims, err := r.Store.ManagedPlatformRecoveryClaims(ctx, item.ID, item.Revision)
	if err != nil {
		return err
	}
	namespace := "managed-platform-" + item.ID
	sets, err := r.Cluster.kube.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=hakopod,hakopod.io/managed-platform-id=" + item.ID, Limit: maxNeonRecoveryWorkloads})
	if err != nil {
		return err
	}
	if sets.Continue != "" {
		return fmt.Errorf("Neon workload inventory exceeds the recovery bound")
	}
	seen := 0
	expectedPods := 0
	for i := range sets.Items {
		set := sets.Items[i].DeepCopy()
		if !strings.HasPrefix(set.Name, "neon-pageserver-") && !strings.HasPrefix(set.Name, "neon-safekeeper-") {
			continue
		}
		seen++
		claim := claims["statefulset."+set.Name]
		if claim.ResourceID != string(set.UID) {
			return fmt.Errorf("Neon storage writer ownership changed")
		}
		desired := pageserverTarget
		if strings.HasPrefix(set.Name, "neon-safekeeper-") {
			desired = safekeeperTarget
		}
		if desired < 0 {
			desired, err = r.Store.PlatformRecoveryPriorReplicas(ctx, op, item.ID, set.Name, string(set.UID))
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
		}
		expectedPods += int(desired)
		if err = r.transitionStatefulSet(ctx, op, item, set, desired, claim.ImmutableGeneration); err != nil {
			return err
		}
	}
	if seen != item.Spec.Neon.Pageservers+item.Spec.Neon.Safekeepers {
		return fmt.Errorf("Neon storage writer inventory changed")
	}
	return r.waitStoragePods(ctx, item.ID, namespace, expectedPods)
}

func (r *NeonRecoveryRuntime) waitStoragePods(ctx context.Context, platformID, namespace string, expected int) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		pods, err := r.Cluster.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=hakopod,hakopod.io/managed-platform-id=" + platformID, Limit: maxNeonRecoveryWorkloads})
		if err != nil {
			return err
		}
		if pods.Continue != "" {
			return fmt.Errorf("Neon pod inventory exceeds the recovery bound")
		}
		count := 0
		for _, p := range pods.Items {
			role := p.Labels["hakopod.io/neon-role"]
			if role != "pageserver" && role != "safekeeper" {
				continue
			}
			if expected == 0 {
				count++
			} else if p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning && allContainersReady(p.Status.ContainerStatuses) {
				count++
			}
		}
		if count == expected {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Neon storage writer transition timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
func (r *NeonRecoveryRuntime) transitionStatefulSet(ctx context.Context, op platformbackup.Operation, item store.ManagedPlatform, set *appsv1.StatefulSet, target int32, claimed int64) error {
	desired := set.DeepCopy()
	desired.Spec.Replicas = &target
	return applyRecoveryStatefulSetMutation(ctx, r.Store, op, item.ID, set, desired, claimed, func() error { return r.fence(ctx, op) }, func(value *appsv1.StatefulSet, options metav1.UpdateOptions) (*appsv1.StatefulSet, error) {
		return r.Cluster.kube.AppsV1().StatefulSets(set.Namespace).Update(ctx, value, options)
	})
}

func (r *NeonRecoveryRuntime) waitServingPods(ctx context.Context, platformID, namespace string, expected int) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		pods, err := r.Cluster.kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=hakopod,hakopod.io/managed-platform-id=" + platformID, Limit: maxNeonRecoveryWorkloads})
		if err != nil {
			return err
		}
		if pods.Continue != "" {
			return fmt.Errorf("Neon pod inventory exceeds the recovery bound")
		}
		count := 0
		for _, p := range pods.Items {
			component := p.Labels["app.kubernetes.io/component"]
			if component == "proxy" || strings.HasPrefix(component, "compute-") {
				if expected == 0 {
					count++
				} else if p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning && allContainersReady(p.Status.ContainerStatuses) {
					count++
				}
			}
		}
		if count == expected {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Neon serving workload transition timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func allContainersReady(status []corev1.ContainerStatus) bool {
	if len(status) == 0 {
		return false
	}
	for _, container := range status {
		if !container.Ready {
			return false
		}
	}
	return true
}

func parseRecoveryLSNOutput(value string) (uint64, error) {
	fields := strings.Fields(value)
	for _, field := range fields {
		parts := strings.Split(strings.TrimSpace(field), "/")
		if len(parts) != 2 {
			continue
		}
		high, highErr := strconv.ParseUint(parts[0], 16, 32)
		low, lowErr := strconv.ParseUint(parts[1], 16, 32)
		if highErr == nil && lowErr == nil {
			return high<<32 | low, nil
		}
	}
	return 0, fmt.Errorf("invalid PostgreSQL LSN")
}

var _ platformbackup.Runtime = (*NeonRecoveryRuntime)(nil)
