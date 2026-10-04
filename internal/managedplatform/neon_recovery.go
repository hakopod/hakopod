package managedplatform

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// NeonRecoveryFence is the immutable point that a remote-storage capture must
// contain. Callers must already have stopped computes and must keep storage
// writers fenced until the object inventory has been copied.
type NeonRecoveryFence struct {
	TenantID           string            `json:"tenant_id"`
	TimelineID         string            `json:"timeline_id"`
	TenantGeneration   int64             `json:"tenant_generation"`
	TimelineGeneration int64             `json:"timeline_generation"`
	CommitLSN          string            `json:"commit_lsn"`
	Pageservers        map[string]string `json:"pageserver_remote_consistent_lsn"`
}

// CheckpointOwnedRecovery derives the ownership token from the durable tenant
// claim, so callers never need to persist or reconstruct that secret token.
func (r *DurableNeonRuntime) CheckpointOwnedRecovery(ctx context.Context, tenantID, timelineID string) (NeonRecoveryFence, error) {
	defer r.closeIdleConnections()
	_, claims, err := r.claims(ctx)
	if err != nil {
		return NeonRecoveryFence{}, err
	}
	claim, ok := claims["tenant"]
	if !ok {
		return NeonRecoveryFence{}, fmt.Errorf("Neon recovery tenant ownership claim is unavailable")
	}
	identity, tenantToken, err := parseNeonOwnedResourceID(claim.ResourceID)
	if err != nil || !strings.HasPrefix(identity, tenantID+"@") {
		return NeonRecoveryFence{}, fmt.Errorf("Neon recovery tenant ownership claim does not match")
	}
	timeline, ok := claims["timeline"]
	if !ok {
		return NeonRecoveryFence{}, fmt.Errorf("Neon recovery timeline ownership claim is unavailable")
	}
	_, timelineToken, err := parseNeonOwnedResourceID(timeline.ResourceID)
	if err != nil {
		return NeonRecoveryFence{}, fmt.Errorf("Neon recovery timeline ownership claim is invalid")
	}
	return r.CheckpointRecovery(ctx, tenantID, timelineID, tenantToken, timelineToken)
}

func NeonRecoveryExternalKey(component, resourceID, tenantID, timelineID string) (string, error) {
	switch component {
	case "tenant":
		return tenantID, nil
	case "timeline":
		return tenantID + "/" + timelineID, nil
	default:
		identity, _, err := parseNeonOwnedResourceID(resourceID)
		if err != nil {
			return "", err
		}
		return identity, nil
	}
}

// CheckpointRecovery flushes every owned safekeeper control file, requires one
// identical committed LSN across the three-member set, then forces the
// controller-selected pageserver to upload through that LSN. Secondary
// pageservers share remote storage but do not serve this unsharded tenant.
func (r *DurableNeonRuntime) CheckpointRecovery(ctx context.Context, tenantID, timelineID, tenantOwnershipToken, timelineOwnershipToken string) (NeonRecoveryFence, error) {
	defer r.closeIdleConnections()
	var fence NeonRecoveryFence
	if r == nil || r.control == nil || !neonID.MatchString(tenantID) || !neonID.MatchString(timelineID) || !validNeonComputeOwnershipToken(tenantOwnershipToken) || !validNeonComputeOwnershipToken(timelineOwnershipToken) || r.control.config.PageserverToken == "" {
		return fence, fmt.Errorf("invalid Neon recovery checkpoint request")
	}
	if len(r.control.config.Safekeepers) != 3 || len(r.control.config.Pageservers) < 2 || len(r.control.config.Pageservers) > 8 {
		return fence, fmt.Errorf("Neon recovery requires exactly three safekeepers and two to eight pageservers")
	}
	tenantExists, attachment, tenantGeneration, tenantToken, tenantState, _, tenantErr := r.inspectTenant(ctx, tenantID)
	if tenantErr != nil || !tenantExists || tenantToken != tenantOwnershipToken || tenantState != "completed" {
		return fence, fmt.Errorf("Neon recovery tenant is not owned by the accepted operation: %w", tenantErr)
	}
	exists, _, generation, _, observedToken, err := r.inspectSafekeeperTimeline(ctx, tenantID, timelineID)
	if err != nil || !exists || observedToken != timelineOwnershipToken {
		return fence, fmt.Errorf("Neon recovery timeline is not owned by the accepted operation: %w", err)
	}
	guard := func() error {
		if err := r.lifecycle.Heartbeat(ctx); err != nil {
			return err
		}
		present, currentAttachment, currentTenantGeneration, currentTenantToken, currentTenantState, _, inspectErr := r.inspectTenant(ctx, tenantID)
		if inspectErr != nil || !present || currentAttachment != attachment || currentTenantGeneration != tenantGeneration || currentTenantToken != tenantOwnershipToken || currentTenantState != "completed" {
			return fmt.Errorf("Neon recovery tenant placement, generation or ownership changed")
		}
		stillExists, _, currentGeneration, _, currentToken, inspectErr := r.inspectSafekeeperTimeline(ctx, tenantID, timelineID)
		if inspectErr != nil || !stillExists || currentGeneration != generation || currentToken != timelineOwnershipToken {
			return fmt.Errorf("Neon recovery ownership or generation changed: %w", inspectErr)
		}
		return nil
	}
	var attached *NeonPageserverRegistration
	for i := range r.control.config.Pageservers {
		node := &r.control.config.Pageservers[i]
		if attachment == tenantID+"@"+strconv.FormatInt(node.NodeID, 10) {
			if attached != nil {
				return fence, fmt.Errorf("Neon recovery pageserver registration is duplicated")
			}
			attached = node
		}
	}
	if attached == nil {
		return fence, fmt.Errorf("Neon recovery tenant is attached to an unowned pageserver")
	}
	path := "/v1/tenant/" + tenantID + "/timeline/" + timelineID
	commit := uint64(0)
	for _, node := range r.control.config.Safekeepers {
		if err = guard(); err != nil {
			return fence, err
		}
		target := NeonControlTarget{Name: "safekeeper-" + node.Name, Origin: "https://" + net.JoinHostPort(node.Host, "7676"), Token: r.control.config.SafekeeperToken}
		if _, status, e := r.control.request(ctx, target, http.MethodPost, path+"/checkpoint", nil); e != nil || status != http.StatusOK {
			return fence, fmt.Errorf("checkpoint Neon safekeeper %s returned HTTP %d: %w", node.Name, status, e)
		}
		body, status, e := r.control.request(ctx, target, http.MethodGet, path, nil)
		if e != nil || status != http.StatusOK {
			return fence, fmt.Errorf("inspect checkpointed Neon safekeeper %s returned HTTP %d: %w", node.Name, status, e)
		}
		var state struct {
			FlushLSN  string `json:"flush_lsn"`
			CommitLSN string `json:"commit_lsn"`
		}
		if json.Unmarshal(body, &state) != nil {
			return fence, fmt.Errorf("decode checkpointed Neon safekeeper %s", node.Name)
		}
		flush, e1 := parseNeonLSN(state.FlushLSN)
		candidate, e2 := parseNeonLSN(state.CommitLSN)
		if e1 != nil || e2 != nil || candidate == 0 || flush < candidate || commit != 0 && commit != candidate {
			return fence, fmt.Errorf("Neon safekeeper committed LSN is incomplete or divergent")
		}
		commit = candidate
	}
	fence = NeonRecoveryFence{TenantID: tenantID, TimelineID: timelineID, TenantGeneration: tenantGeneration, TimelineGeneration: generation, CommitLSN: formatNeonLSN(commit), Pageservers: map[string]string{}}
	for _, node := range []NeonPageserverRegistration{*attached} {
		if err = guard(); err != nil {
			return NeonRecoveryFence{}, err
		}
		target := NeonControlTarget{Name: "pageserver-" + node.Name, Origin: "https://" + net.JoinHostPort(node.Host, "9898"), Token: r.control.config.PageserverToken}
		wait := map[string]any{"timelines": map[string]string{timelineID: fence.CommitLSN}, "timeout": r.control.config.RequestTimeout.String()}
		if _, e := r.control.doJSON(ctx, target, http.MethodPost, "/v1/tenant/"+tenantID+"/wait_lsn", wait, http.StatusOK); e != nil {
			return NeonRecoveryFence{}, fmt.Errorf("wait for Neon pageserver %s recovery LSN: %w", node.Name, e)
		}
		checkpoint := path + "/checkpoint?compact=false&wait_until_flushed=true&wait_until_uploaded=true"
		if err = guard(); err != nil {
			return NeonRecoveryFence{}, err
		}
		if _, status, e := r.control.request(ctx, target, http.MethodPost, checkpoint, nil); e != nil || status != http.StatusOK {
			return NeonRecoveryFence{}, fmt.Errorf("checkpoint Neon pageserver %s returned HTTP %d: %w", node.Name, status, e)
		}
		body, status, e := r.control.request(ctx, target, http.MethodGet, path, nil)
		if e != nil || status != http.StatusOK {
			return NeonRecoveryFence{}, fmt.Errorf("inspect checkpointed Neon pageserver %s returned HTTP %d: %w", node.Name, status, e)
		}
		var state struct {
			RemoteConsistentLSNVisible string `json:"remote_consistent_lsn_visible"`
		}
		if json.Unmarshal(body, &state) != nil {
			return NeonRecoveryFence{}, fmt.Errorf("decode checkpointed Neon pageserver %s", node.Name)
		}
		visible, parseErr := parseNeonLSN(state.RemoteConsistentLSNVisible)
		if parseErr != nil || visible < commit {
			return NeonRecoveryFence{}, fmt.Errorf("Neon pageserver %s did not upload through the recovery LSN", node.Name)
		}
		fence.Pageservers[node.Name] = formatNeonLSN(visible)
	}
	if err = guard(); err != nil {
		return NeonRecoveryFence{}, err
	}
	return fence, nil
}

func parseNeonLSN(value string) (uint64, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, fmt.Errorf("invalid Neon LSN")
	}
	hi, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid Neon LSN")
	}
	lo, err := strconv.ParseUint(parts[1], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid Neon LSN")
	}
	return hi<<32 | lo, nil
}

func formatNeonLSN(value uint64) string { return fmt.Sprintf("%X/%X", value>>32, value&0xffffffff) }
