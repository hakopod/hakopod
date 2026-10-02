package api

import (
	"context"
	"crypto/subtle"
	"fmt"
	"reflect"
	"strconv"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

type SupabaseSecretSnapshotResolver func(context.Context, store.Principal, store.ManagedPlatform, string, managedplatform.SecretReference) (map[string][]byte, error)

type NativeManagedPlatformPlanner struct {
	Store                         *store.Store
	EncryptionKey                 []byte
	SupabaseImages                map[string]string
	SupabaseIdentities            map[string]managedplatform.RuntimeIdentity
	ApprovedEncryptedStorageClass string
	SharedStorageGID              int64
	ApprovedExternalHTTPSCIDRs    []string
	ResolveSupabaseSecret         SupabaseSecretSnapshotResolver
	NeonImages                    map[string]string
	NeonIdentities                map[string]managedplatform.NeonRuntimeIdentity
	NeonProxyControlPlaneOrigin   string
	NeonControlPlaneNamespace     string
	NeonControlPlanePodLabels     map[string]string
	NeonProxyToken                string
	NeonProxyEndpoints            map[string]NeonProxyBootstrapConfig
	ResolveNeonSecret             SupabaseSecretSnapshotResolver
	CatalogNodes                  []managedplatform.CapacityNode
	CatalogSecrets                map[string]map[string][]managedplatform.SecretReference
}

func (p *NativeManagedPlatformPlanner) PlanManagedPlatform(_ context.Context, _ store.Principal, item store.ManagedPlatform, _ int64, _ string) (managedplatform.Plan, error) {
	switch item.Spec.Kind {
	case "supabase":
		plan, err := managedplatform.PlanSupabase(item.Spec, p.SupabaseImages)
		plan.Namespace = "managed-platform-" + item.ID
		plan.StorageClass = p.ApprovedEncryptedStorageClass
		return plan, err
	case "neon":
		plan, err := managedplatform.PlanNeon(item.Spec, p.NeonImages)
		plan.Namespace = "managed-platform-" + item.ID
		plan.StorageClass = p.ApprovedEncryptedStorageClass
		return plan, err
	default:
		return managedplatform.Plan{}, fmt.Errorf("managed platform kind is not configured")
	}
}
func (p *NativeManagedPlatformPlanner) SealManagedPlatformSnapshot(ctx context.Context, principal store.Principal, item store.ManagedPlatform, reviewed managedplatform.Plan, expected int64, kind string) ([]byte, error) {
	plan, err := p.PlanManagedPlatform(ctx, principal, item, expected, kind)
	if err != nil || !reflect.DeepEqual(plan, reviewed) {
		return nil, fmt.Errorf("managed platform plan changed after review")
	}
	var previous *managedplatform.Spec
	if expected > 0 {
		if p.Store == nil {
			return nil, fmt.Errorf("managed platform history is unavailable")
		}
		current, getErr := p.Store.ManagedPlatform(ctx, principal, item.ID, true)
		if getErr != nil {
			return nil, getErr
		}
		value := current.Spec
		previous = &value
	}
	switch item.Spec.Kind {
	case "supabase":
		if p.ResolveSupabaseSecret == nil {
			return nil, fmt.Errorf("Supabase runtime resolution is unavailable")
		}
		render := managedplatform.SupabaseRenderInput{Spec: item.Spec, PlatformID: item.ID, Images: cloneManagedPlatformStrings(p.SupabaseImages), Revision: expected + 1, Identities: cloneSupabaseIdentities(p.SupabaseIdentities), ApprovedEncryptedStorageClass: p.ApprovedEncryptedStorageClass, SharedStorageGID: p.SharedStorageGID, ApprovedExternalHTTPSCIDRs: append([]string(nil), p.ApprovedExternalHTTPSCIDRs...), PreviousSpec: previous}
		snapshots, resolveErr := resolveManagedPlatformSecrets(ctx, p.ResolveSupabaseSecret, principal, item, "Supabase")
		if resolveErr != nil {
			return nil, resolveErr
		}
		if err = validateSupabaseAdministrativeAuthority(item.Spec, snapshots); err != nil {
			return nil, err
		}
		request := cluster.SupabaseRuntimeRequest{Render: render, SecretSnapshots: snapshots}
		return cluster.SealManagedPlatformSnapshot(p.EncryptionKey, item.ID, expected+1, kind, cluster.ManagedPlatformSnapshot{ReviewedPlan: reviewed, Supabase: &request})
	case "neon":
		if p.ResolveNeonSecret == nil {
			return nil, fmt.Errorf("Neon runtime resolution is unavailable")
		}
		render := managedplatform.NeonRenderInput{Spec: item.Spec, PlatformID: item.ID, Images: cloneManagedPlatformStrings(p.NeonImages), Revision: expected + 1, Identities: cloneNeonIdentities(p.NeonIdentities), ApprovedEncryptedStorageClass: p.ApprovedEncryptedStorageClass, SharedStorageGID: p.SharedStorageGID, ProxyControlPlaneOrigin: p.NeonProxyControlPlaneOrigin, ControlPlaneNamespace: p.NeonControlPlaneNamespace, ControlPlanePodLabels: cloneManagedPlatformStrings(p.NeonControlPlanePodLabels), ApprovedExternalHTTPSCIDRs: append([]string(nil), p.ApprovedExternalHTTPSCIDRs...), PreviousSpec: previous}
		snapshots, resolveErr := resolveManagedPlatformSecrets(ctx, p.ResolveNeonSecret, principal, item, "Neon")
		if resolveErr != nil {
			return nil, resolveErr
		}
		bootstrap := managedplatform.NeonProxyBootstrapState{EndpointID: item.ID}
		if kind != "delete" {
			proxyRef, ok := item.Spec.Secrets["proxy-auth"]
			if !ok {
				return nil, fmt.Errorf("Neon proxy secret reference is unavailable")
			}
			proxySnapshot := proxyRef.Name + "-r" + strconv.FormatInt(proxyRef.Revision, 10)
			if !ConstantTimeNeonProxyToken(p.NeonProxyToken, string(snapshots[proxySnapshot]["token"])) {
				return nil, fmt.Errorf("Neon proxy token does not match its immutable secret snapshot")
			}
			scope := item.Project + "/" + item.Environment + "/" + item.Spec.Name
			proxy, ok := p.NeonProxyEndpoints[scope]
			if !ok {
				return nil, fmt.Errorf("Neon proxy bootstrap %s is unavailable", scope)
			}
			if err = ValidateNeonProxyBootstrap(proxy); err != nil {
				return nil, err
			}
			roles := make(map[string]managedplatform.NeonProxyRoleState, len(proxy.Roles))
			for name, value := range proxy.Roles {
				roles[name] = managedplatform.NeonProxyRoleState{SCRAMSecret: value.SCRAMSecret, AllowedIPs: append([]string(nil), value.AllowedIPs...), AllowedVPCEndpointIDs: append([]string(nil), value.AllowedVPCEndpointIDs...), BlockPublicConnections: value.BlockPublicConnections, BlockVPCConnections: value.BlockVPCConnections}
			}
			bootstrap.Enabled = proxy.Enabled
			bootstrap.Roles = roles
		}
		request := cluster.NeonRuntimeRequest{Render: render, SecretSnapshots: snapshots, ProxyEndpoint: bootstrap}
		return cluster.SealManagedPlatformSnapshot(p.EncryptionKey, item.ID, expected+1, kind, cluster.ManagedPlatformSnapshot{ReviewedPlan: reviewed, Neon: &request})
	default:
		return nil, fmt.Errorf("managed platform kind is not configured")
	}
}

func validateSupabaseAdministrativeAuthority(spec managedplatform.Spec, snapshots map[string]map[string][]byte) error {
	applicationRef, applicationOK := spec.Secrets["jwt-secret"]
	adminRef, adminOK := spec.Secrets["pooler-api-jwt-secret"]
	if !applicationOK || !adminOK || applicationRef == adminRef {
		return fmt.Errorf("Supabase pooler administrative JWT secret must use a separate immutable revision")
	}
	name := func(ref managedplatform.SecretReference) string {
		return ref.Name + "-r" + strconv.FormatInt(ref.Revision, 10)
	}
	application := snapshots[name(applicationRef)]["value"]
	admin := snapshots[name(adminRef)]["value"]
	if len(application) == 0 || len(admin) < 32 || (len(application) == len(admin) && subtle.ConstantTimeCompare(application, admin) == 1) {
		return fmt.Errorf("Supabase pooler administrative JWT secret must contain independent key material of at least 32 bytes")
	}
	return nil
}
func cloneManagedPlatformStrings(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
func cloneSupabaseIdentities(in map[string]managedplatform.RuntimeIdentity) map[string]managedplatform.RuntimeIdentity {
	out := make(map[string]managedplatform.RuntimeIdentity, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
func cloneNeonIdentities(in map[string]managedplatform.NeonRuntimeIdentity) map[string]managedplatform.NeonRuntimeIdentity {
	out := make(map[string]managedplatform.NeonRuntimeIdentity, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
func copyManagedPlatformSecret(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for key, value := range in {
		out[key] = append([]byte(nil), value...)
	}
	return out
}
func resolveManagedPlatformSecrets(ctx context.Context, resolver SupabaseSecretSnapshotResolver, principal store.Principal, item store.ManagedPlatform, label string) (map[string]map[string][]byte, error) {
	snapshots := map[string]map[string][]byte{}
	for key := range item.Spec.Secrets {
		ref := item.Spec.Secrets[key]
		name := ref.Name + "-r" + strconv.FormatInt(ref.Revision, 10)
		data, err := resolver(ctx, principal, item, key, ref)
		if err != nil {
			return nil, fmt.Errorf("resolve %s secret %s: %w", label, key, err)
		}
		copy := copyManagedPlatformSecret(data)
		if old, ok := snapshots[name]; ok && !reflect.DeepEqual(old, copy) {
			return nil, fmt.Errorf("%s secret snapshot %s resolved inconsistently", label, name)
		}
		snapshots[name] = copy
	}
	return snapshots, nil
}
