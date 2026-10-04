package managedplatform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"
)

// The pinned controller uses node IDs, never caller-selected network addresses.
// Hakopod currently provisions one unsharded tenant and three safekeepers.
type NeonAttachNotification struct {
	TenantID    string            `json:"tenant_id"`
	PreferredAZ *string           `json:"preferred_az"`
	StripeSize  *uint32           `json:"stripe_size"`
	Shards      []NeonAttachShard `json:"shards"`
}

type NeonAttachShard struct {
	NodeID      int64 `json:"node_id"`
	ShardNumber uint8 `json:"shard_number"`
}

type NeonSafekeeperNotification struct {
	TenantID    string                 `json:"tenant_id"`
	TimelineID  string                 `json:"timeline_id"`
	Generation  int64                  `json:"generation"`
	Safekeepers []NeonSafekeeperMember `json:"safekeepers"`
}

type NeonSafekeeperMember struct {
	ID       int64   `json:"id"`
	Hostname *string `json:"hostname"`
}

type NeonControllerState struct {
	Attach      *NeonAttachNotification     `json:"attach,omitempty"`
	Safekeepers *NeonSafekeeperNotification `json:"safekeepers,omitempty"`
}

func NeonControllerToken(key []byte, platformID string, revision int64) (string, error) {
	if len(key) != 32 || !neonID.MatchString(platformID) || revision < 1 {
		return "", fmt.Errorf("invalid Neon controller identity")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "hakopod-neon-controller-v1\x00%s\x00%d", platformID, revision)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func ValidNeonControllerToken(key []byte, platformID string, revision int64, token string) bool {
	expected, err := NeonControllerToken(key, platformID, revision)
	return err == nil && len(token) == len(expected) && subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func (s NeonControllerState) Validate(tenantID, timelineID string, pageservers int) error {
	if !neonID.MatchString(tenantID) || !neonID.MatchString(timelineID) || pageservers < 2 || pageservers > 8 {
		return fmt.Errorf("invalid Neon controller binding")
	}
	if a := s.Attach; a != nil {
		if a.TenantID != tenantID || len(a.Shards) != 1 || a.Shards[0].ShardNumber != 0 || a.Shards[0].NodeID < 1 || a.Shards[0].NodeID > int64(pageservers) || a.StripeSize != nil || a.PreferredAZ != nil && len(*a.PreferredAZ) > 63 {
			return fmt.Errorf("Neon attachment does not match the owned unsharded tenant")
		}
	}
	if sk := s.Safekeepers; sk != nil {
		if sk.TenantID != tenantID || sk.TimelineID != timelineID || sk.Generation < 0 || sk.Generation > 4294967295 || len(sk.Safekeepers) != 3 {
			return fmt.Errorf("Neon safekeeper notification does not match the owned timeline")
		}
		seen := map[int64]bool{}
		for _, member := range sk.Safekeepers {
			if member.ID < 1 || member.ID > 3 || seen[member.ID] || member.Hostname != nil && len(*member.Hostname) > 253 {
				return fmt.Errorf("Neon safekeeper notification contains an unowned node")
			}
			seen[member.ID] = true
		}
	}
	return nil
}

// BindNeonControllerRouting removes stale template routing and derives both
// data paths from the durable node-ID notification and owned service names.
func BindNeonControllerRouting(raw json.RawMessage, platformID, tenantID, timelineID string, pageservers int, state NeonControllerState) (json.RawMessage, error) {
	if !neonID.MatchString(platformID) || state.Validate(tenantID, timelineID, pageservers) != nil || state.Attach == nil || state.Safekeepers == nil {
		return nil, fmt.Errorf("Neon controller routing is not ready")
	}
	var root map[string]any
	if len(raw) > maxNeonResponseBytes || decodeNeonJSON(raw, &root) != nil {
		return nil, fmt.Errorf("invalid Neon compute configuration")
	}
	spec, ok := root["spec"].(map[string]any)
	if !ok || spec["tenant_id"] != tenantID || spec["timeline_id"] != timelineID {
		return nil, fmt.Errorf("Neon compute configuration identity changed")
	}
	namespace := "managed-platform-" + platformID
	u := url.URL{Scheme: "postgresql", Host: fmt.Sprintf("neon-pageserver-%d.%s.svc:6400", state.Attach.Shards[0].NodeID-1, namespace)}
	u.RawQuery = url.Values{"sslmode": {"verify-full"}, "sslrootcert": {"/var/run/secrets/hakopod/pageserver-auth/ca.crt"}}.Encode()
	delete(spec, "pageserver_connection_info")
	delete(spec, "shard_stripe_size")
	spec["pageserver_connstring"] = u.String()
	members := append([]NeonSafekeeperMember(nil), state.Safekeepers.Safekeepers...)
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	hosts := make([]string, len(members))
	for i, member := range members {
		hosts[i] = fmt.Sprintf("neon-safekeeper-%d.%s.svc:5454", member.ID-1, namespace)
	}
	spec["safekeepers_generation"] = state.Safekeepers.Generation
	if err := bindNeonWALRouting(spec, hosts); err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

// ApplyNeonComputeNotification changes only durably owned computes. It also
// replays Empty computes after restart, using the provider's durable spec fence.
func ApplyNeonComputeNotification(ctx context.Context, targets []NeonControlTarget, roots *x509.CertPool, configs map[string]json.RawMessage, claims []DurableResourceClaim, tenantID, timelineID string) error {
	if roots == nil || len(targets) < 1 || len(targets) > maxNeonComputeNodes || len(claims) > maxDurableNeonResources {
		return fmt.Errorf("invalid Neon compute notification inventory")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, MaxConnsPerHost: 2, MaxIdleConns: 2, TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 2 * time.Second, MaxResponseHeaderBytes: 32 << 10}
	defer transport.CloseIdleConnections()
	r := &NeonRuntime{config: NeonRuntimeConfig{RequestTimeout: 2 * time.Second}, client: &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("Neon redirects are not permitted") }}}
	byComponent := map[string]DurableResourceClaim{}
	for _, claim := range claims {
		if _, duplicate := byComponent[claim.Component]; duplicate {
			return fmt.Errorf("duplicate Neon compute ownership")
		}
		byComponent[claim.Component] = claim
	}
	applyTarget := func(target NeonControlTarget) error {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := validateNeonTarget(target, "compute"); err != nil {
			return err
		}
		claim, ok := byComponent["compute-"+target.Name]
		if !ok {
			return nil
		}
		_, identity, err := neonComputeIdentity(target)
		claimed, token, parseErr := parseNeonComputeClaimResourceID(claim.ResourceID)
		if err != nil || parseErr != nil || claimed != identity || claim.ImmutableGeneration != 1 || claim.Kind != "runtime_component" {
			return fmt.Errorf("Neon compute ownership changed")
		}
		body, status, err := r.request(ctx, target, http.MethodGet, "/status", nil)
		if err != nil || status != http.StatusOK {
			return fmt.Errorf("Neon compute status is unavailable")
		}
		if verifyNeonComputeOwnershipStatus(body, token) != nil {
			return fmt.Errorf("Neon compute ownership changed")
		}
		configuration, err := bindNeonComputeOwnership(configs[target.Name], token)
		if err != nil {
			return err
		}
		return r.configureOwnedCompute(ctx, target, configuration, token, tenantID, timelineID, body, false)
	}
	// Six bounded targets run in two waves. A failing early target must not
	// prevent later targets from making progress on every controller retry.
	semaphore := make(chan struct{}, 3)
	failures := make(chan error, len(targets))
	var workers sync.WaitGroup
	for _, target := range targets {
		workers.Add(1)
		go func(target NeonControlTarget) {
			defer workers.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				failures <- ctx.Err()
				return
			}
			if err := applyTarget(target); err != nil {
				failures <- err
			}
		}(target)
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		return err
	}
	return nil
}

func NeonControllerRevision(value string) (int64, error) {
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != value {
		return 0, fmt.Errorf("invalid Neon controller revision")
	}
	return revision, nil
}
