package managedplatform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	maxNeonResponseBytes = 1 << 20
	maxNeonComputeNodes  = 6
)

var neonID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// NeonControlTarget is one private, TLS-protected Neon management listener.
// Token is used only in memory and is never written to lifecycle state.
type NeonControlTarget struct {
	Name   string
	Origin string
	Token  string
}

// NeonRuntimeConfig connects Hakopod to one self-hosted Neon control plane.
// The storage controller is the only component called directly for tenant,
// timeline, pageserver, and safekeeper placement.
type NeonRuntimeConfig struct {
	StorageController                        NeonControlTarget
	Computes                                 []NeonControlTarget
	Pageservers                              []NeonPageserverRegistration
	Safekeepers                              []NeonSafekeeperRegistration
	SafekeeperToken                          string
	StateDirectory                           string
	RequestTimeout                           time.Duration
	RootCAs                                  *x509.CertPool
	DeprovisionOnly                          bool
	allowMissingStorageRegistrationsForTest  bool
	allowUnqualifiedOwnershipProtocolForTest bool
	// TestOnlyFileState must never be enabled by the API or reconciler. The
	// production adapter requires PostgreSQL ownership fencing instead.
	TestOnlyFileState bool
}

type NeonPageserverRegistration struct {
	Name             string
	NodeID           int64
	Generation       int64
	Host             string
	AvailabilityZone string
}

type NeonSafekeeperRegistration struct {
	Name             string
	NodeID           int64
	Generation       int64
	Host             string
	AvailabilityZone string
}

// NeonLifecycleRequest uses caller-assigned immutable Neon IDs. ComputeConfig
// contains the exact upstream compute_ctl ConfigurationRequest JSON.
type NeonLifecycleRequest struct {
	OperationID        string
	TenantID           string
	TimelineID         string
	AncestorTimelineID string
	CreateTenant       bool
	TenantOwnership    string
	ComputeConfig      map[string]json.RawMessage
}

// NeonLifecycleState is durable orchestration evidence, not availability or
// backup qualification. Secrets and compute configuration are deliberately
// excluded.
type NeonLifecycleState struct {
	SchemaVersion     int       `json:"schema_version"`
	OperationID       string    `json:"operation_id"`
	RequestDigest     string    `json:"request_digest"`
	TenantID          string    `json:"tenant_id"`
	TimelineID        string    `json:"timeline_id"`
	TenantRequested   bool      `json:"tenant_requested"`
	TenantCreated     bool      `json:"tenant_created"`
	TenantReady       bool      `json:"tenant_ready"`
	TimelineRequested bool      `json:"timeline_requested"`
	TimelineCreated   bool      `json:"timeline_created"`
	SafekeeperCount   int       `json:"safekeeper_count"`
	SafekeeperHosts   []string  `json:"safekeeper_hosts"`
	AttachedComputes  []string  `json:"attached_computes"`
	AttachingCompute  string    `json:"attaching_compute,omitempty"`
	Complete          bool      `json:"complete"`
	RolledBack        bool      `json:"rolled_back"`
	LastError         string    `json:"last_error,omitempty"`
	CleanupRequired   bool      `json:"cleanup_required"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// NeonRuntimeQualification remains false until the native runtime, durable
// API store, object-storage recovery, backup/restore, and real-cluster cases
// have all passed. Exercising this adapter alone cannot change that gate.
func NeonRuntimeQualification() Capability {
	return Capability{Available: false, ClusterQualified: false, PublicQualified: false, Reason: "Neon runtime, durable API state, object-storage recovery, backup and real-cluster acceptance remain unqualified"}
}

type NeonRuntime struct {
	config NeonRuntimeConfig
	client *http.Client
}

func NewNeonRuntime(config NeonRuntimeConfig) (*NeonRuntime, error) {
	if !config.TestOnlyFileState {
		return nil, fmt.Errorf("file-backed Neon lifecycle is test-only; production requires PostgreSQL ownership fencing")
	}
	if err := validateNeonTarget(config.StorageController, "storage controller"); err != nil {
		return nil, err
	}
	if len(config.Computes) == 0 || len(config.Computes) > maxNeonComputeNodes {
		return nil, fmt.Errorf("Neon runtime requires 1-%d compute targets", maxNeonComputeNodes)
	}
	seen := map[string]bool{}
	for _, target := range config.Computes {
		if err := validateNeonTarget(target, "compute"); err != nil {
			return nil, err
		}
		if seen[target.Name] {
			return nil, fmt.Errorf("duplicate Neon compute target %q", target.Name)
		}
		seen[target.Name] = true
	}
	if !filepath.IsAbs(config.StateDirectory) || filepath.Clean(config.StateDirectory) != config.StateDirectory {
		return nil, fmt.Errorf("Neon state directory must be an absolute clean path")
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 30 * time.Second
	}
	if config.RequestTimeout < time.Second || config.RequestTimeout > 2*time.Minute {
		return nil, fmt.Errorf("Neon request timeout must be between one second and two minutes")
	}
	if config.RootCAs == nil {
		return nil, fmt.Errorf("Neon runtime requires an explicit control-plane CA pool")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: config.RootCAs}, MaxIdleConns: 8, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: config.RequestTimeout / 2, MaxResponseHeaderBytes: 32 << 10}
	client := &http.Client{Transport: transport, Timeout: config.RequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("Neon control redirects are not permitted")
	}}
	return &NeonRuntime{config: config, client: client}, nil
}

func validateNeonTarget(target NeonControlTarget, role string) error {
	if target.Name == "" || len(target.Name) > 63 || len(validation.IsDNS1123Label(target.Name)) != 0 {
		return fmt.Errorf("%s requires a valid DNS label name", role)
	}
	u, err := url.Parse(target.Origin)
	if err != nil || len(target.Origin) > 2048 || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || u.Path != "" && u.Path != "/" {
		return fmt.Errorf("%s requires an exact HTTPS origin without credentials", role)
	}
	host := u.Hostname()
	if host == "" || net.ParseIP(host) == nil && len(validation.IsDNS1123Subdomain(host)) != 0 {
		return fmt.Errorf("%s requires a valid hostname", role)
	}
	if port := u.Port(); port != "" {
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 1 || value > 65535 {
			return fmt.Errorf("%s has an invalid port", role)
		}
	}
	if len(target.Token) < 16 || len(target.Token) > 8192 || strings.ContainsAny(target.Token, "\r\n") {
		return fmt.Errorf("%s requires a bounded bearer token", role)
	}
	return nil
}

func (r *NeonRuntime) Provision(ctx context.Context, request NeonLifecycleRequest) (state NeonLifecycleState, err error) {
	digest, err := validateNeonLifecycleRequest(request, r.config.Computes)
	if err != nil {
		return state, err
	}
	if err = r.verifyOwnershipCapability(ctx); err != nil {
		return state, err
	}
	configSum := sha256.Sum256([]byte(digest + "\x00" + r.config.StorageController.Name + "\x00" + r.config.StorageController.Origin))
	digest = hex.EncodeToString(configSum[:])
	if err := os.MkdirAll(r.config.StateDirectory, 0700); err != nil {
		return state, fmt.Errorf("create Neon state directory: %w", err)
	}
	info, err := os.Lstat(r.config.StateDirectory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return state, fmt.Errorf("Neon state directory must be a private real directory")
	}
	unlock, err := r.lock(request.TenantID)
	if err != nil {
		return state, err
	}
	defer unlock()

	state, err = r.loadOrCreateState(request, digest)
	if err != nil {
		return state, err
	}
	if state.Complete {
		return state, nil
	}
	if state.CleanupRequired {
		return state, fmt.Errorf("Neon operation requires ownership reconciliation before retry")
	}
	if state.RolledBack {
		state.RolledBack = false
		if err = r.storeState(state); err != nil {
			return state, err
		}
	}
	defer func() {
		if err == nil {
			return
		}
		state.LastError = boundedError(err)
		cleanupContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		rollbackErr := r.rollback(cleanupContext, request, &state)
		if rollbackErr != nil {
			state.LastError = boundedError(errors.Join(err, rollbackErr))
		}
		_ = r.storeState(state)
	}()

	if !state.TenantReady {
		exists, checkErr := r.tenantExists(ctx, request.TenantID)
		if checkErr != nil {
			return state, checkErr
		}
		if !request.CreateTenant {
			if !exists {
				return state, fmt.Errorf("branch tenant does not exist")
			}
			state.TenantReady = true
			if err = r.storeState(state); err != nil {
				return state, err
			}
		} else {
			if exists {
				return state, fmt.Errorf("Neon tenant already exists; file state cannot prove ownership")
			}
			state.TenantRequested = true
			if err = r.storeState(state); err != nil {
				return state, err
			}
			body := map[string]any{"new_tenant_id": request.TenantID}
			if _, err = r.doJSON(ctx, r.config.StorageController, http.MethodPost, "/v1/tenant", body, http.StatusCreated); err != nil {
				return state, fmt.Errorf("create Neon tenant: %w", err)
			}
			state.TenantCreated = true
			state.TenantReady = true
			if err = r.storeState(state); err != nil {
				return state, err
			}
		}
	}

	if !state.TimelineCreated {
		exists, checkErr := r.timelineExists(ctx, request.TenantID, request.TimelineID)
		if checkErr != nil {
			return state, checkErr
		}
		if exists {
			return state, fmt.Errorf("Neon timeline already exists; file state cannot prove ownership")
		}
		if !state.TimelineRequested {
			state.TimelineRequested = true
			if err = r.storeState(state); err != nil {
				return state, err
			}
		}
		body := map[string]any{"new_timeline_id": request.TimelineID, "pg_version": 17}
		if request.AncestorTimelineID != "" {
			body["ancestor_timeline_id"] = request.AncestorTimelineID
			body["read_only"] = false
		}
		response, createErr := r.doJSON(ctx, r.config.StorageController, http.MethodPost, "/v1/tenant/"+request.TenantID+"/timeline", body, http.StatusCreated)
		if createErr != nil {
			return state, fmt.Errorf("create Neon timeline: %w", createErr)
		}
		state.SafekeeperHosts, err = verifiedSafekeepers(response, request.TenantID, request.TimelineID)
		if err != nil {
			return state, err
		}
		state.SafekeeperCount = len(state.SafekeeperHosts)
		state.TimelineCreated = true
		if err = r.storeState(state); err != nil {
			return state, err
		}
	}

	attached := make(map[string]bool, len(state.AttachedComputes))
	for _, name := range state.AttachedComputes {
		attached[name] = true
	}
	for _, target := range r.config.Computes {
		if attached[target.Name] {
			continue
		}
		state.AttachingCompute = target.Name
		if err = r.storeState(state); err != nil {
			return state, err
		}
		if _, err = r.doRawJSON(ctx, target, http.MethodPost, "/configure", request.ComputeConfig[target.Name], http.StatusOK); err != nil {
			return state, fmt.Errorf("attach Neon compute %s: %w", target.Name, err)
		}
		status, statusErr := r.doJSON(ctx, target, http.MethodGet, "/status", nil, http.StatusOK)
		if statusErr != nil {
			return state, fmt.Errorf("verify Neon compute %s: %w", target.Name, statusErr)
		}
		if err = verifyComputeStatus(status, request.TenantID, request.TimelineID); err != nil {
			return state, fmt.Errorf("verify Neon compute %s: %w", target.Name, err)
		}
		state.AttachedComputes = append(state.AttachedComputes, target.Name)
		state.AttachingCompute = ""
		sort.Strings(state.AttachedComputes)
		if err = r.storeState(state); err != nil {
			return state, err
		}
	}
	state.Complete = true
	state.LastError = ""
	err = r.storeState(state)
	return state, err
}

func validateNeonLifecycleRequest(request NeonLifecycleRequest, targets []NeonControlTarget) (string, error) {
	if !neonID.MatchString(request.OperationID) || !neonID.MatchString(request.TenantID) || !neonID.MatchString(request.TimelineID) {
		return "", fmt.Errorf("operation, tenant, and timeline IDs must be 32 lowercase hexadecimal characters")
	}
	if request.AncestorTimelineID != "" && !neonID.MatchString(request.AncestorTimelineID) {
		return "", fmt.Errorf("ancestor timeline ID must be 32 lowercase hexadecimal characters")
	}
	if request.AncestorTimelineID == request.TimelineID {
		return "", fmt.Errorf("a Neon branch cannot be its own ancestor")
	}
	if len(request.ComputeConfig) != len(targets) {
		return "", fmt.Errorf("compute configuration must exactly match configured targets")
	}
	canonical := make(map[string]json.RawMessage, len(request.ComputeConfig))
	for _, target := range targets {
		raw, ok := request.ComputeConfig[target.Name]
		if !ok || len(raw) == 0 || len(raw) > maxNeonResponseBytes || !json.Valid(raw) {
			return "", fmt.Errorf("compute %s requires bounded valid configuration JSON", target.Name)
		}
		if err := validateComputeConfig(raw, request.TenantID, request.TimelineID); err != nil {
			return "", fmt.Errorf("compute %s: %w", target.Name, err)
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		canonical[target.Name] = encoded
	}
	if !request.CreateTenant && request.TenantOwnership == "" {
		return "", fmt.Errorf("branching an existing tenant requires an external ownership reference")
	}
	origins := map[string]string{}
	for _, target := range targets {
		origins[target.Name] = target.Origin
	}
	payload := struct {
		OperationID, TenantID, TimelineID, AncestorTimelineID, TenantOwnership string
		CreateTenant                                                           bool
		ComputeConfig                                                          map[string]json.RawMessage
		Origins                                                                map[string]string
	}{request.OperationID, request.TenantID, request.TimelineID, request.AncestorTimelineID, request.TenantOwnership, request.CreateTenant, canonical, origins}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func validateComputeConfig(raw json.RawMessage, tenantID, timelineID string) error {
	var root struct {
		Spec *struct {
			TenantID     string   `json:"tenant_id"`
			TimelineID   string   `json:"timeline_id"`
			Safekeepers  []string `json:"safekeeper_connstrings"`
			StorageToken string   `json:"storage_auth_token"`
		} `json:"spec"`
		ComputeCtlConfig json.RawMessage `json:"compute_ctl_config"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	if root.Spec == nil || root.Spec.TenantID != tenantID || root.Spec.TimelineID != timelineID {
		return fmt.Errorf("compute spec must attach the requested tenant and timeline")
	}
	if len(root.Spec.Safekeepers) < 3 || len(root.Spec.Safekeepers) > 8 {
		return fmt.Errorf("compute spec requires 3-8 safekeeper connections")
	}
	seen := map[string]bool{}
	for _, connection := range root.Spec.Safekeepers {
		host, port, splitErr := net.SplitHostPort(connection)
		portNumber, portErr := strconv.Atoi(port)
		if splitErr != nil || host == "" || portErr != nil || portNumber < 1 || portNumber > 65535 || len(connection) > 2048 || strings.ContainsAny(connection, "\r\n") || seen[connection] {
			return fmt.Errorf("compute spec contains an invalid safekeeper connection")
		}
		seen[connection] = true
	}
	if root.Spec.StorageToken == "" || len(root.Spec.StorageToken) > 8192 {
		return fmt.Errorf("compute spec requires a bounded storage token")
	}
	if len(root.ComputeCtlConfig) == 0 || string(root.ComputeCtlConfig) == "null" {
		return fmt.Errorf("compute_ctl_config is required")
	}
	return nil
}

func verifiedSafekeepers(body []byte, tenantID, timelineID string) ([]string, error) {
	var response struct {
		Safekeepers *struct {
			TenantID    string `json:"tenant_id"`
			TimelineID  string `json:"timeline_id"`
			Safekeepers []struct {
				ID       json.Number `json:"id"`
				Hostname string      `json:"hostname"`
			} `json:"safekeepers"`
		} `json:"safekeepers"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode Neon safekeeper placement: %w", err)
	}
	if response.Safekeepers == nil || response.Safekeepers.TenantID != tenantID || response.Safekeepers.TimelineID != timelineID {
		return nil, fmt.Errorf("Neon timeline response omitted matching safekeeper placement")
	}
	hosts := map[string]bool{}
	ids := map[string]bool{}
	for _, member := range response.Safekeepers.Safekeepers {
		id := member.ID.String()
		if id == "" || id == "0" || member.Hostname == "" || len(member.Hostname) > 255 || hosts[member.Hostname] || ids[id] {
			return nil, fmt.Errorf("Neon returned invalid safekeeper placement")
		}
		hosts[member.Hostname] = true
		ids[id] = true
	}
	if len(hosts) < 3 || len(hosts) > 8 {
		return nil, fmt.Errorf("Neon returned %d safekeepers; expected 3-8", len(hosts))
	}
	result := make([]string, 0, len(hosts))
	for host := range hosts {
		result = append(result, host)
	}
	sort.Strings(result)
	return result, nil
}

func verifyComputeStatus(body []byte, tenantID, timelineID string) error {
	var status struct {
		Tenant   string `json:"tenant"`
		Timeline string `json:"timeline"`
		Status   string `json:"status"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return err
	}
	if status.Status != "running" || status.Tenant != tenantID || status.Timeline != timelineID {
		return fmt.Errorf("compute is not running on the requested tenant and timeline")
	}
	return nil
}

func (r *NeonRuntime) tenantExists(ctx context.Context, tenantID string) (bool, error) {
	return r.exists(ctx, r.config.StorageController, "/control/v1/tenant/"+tenantID)
}

func (r *NeonRuntime) timelineExists(ctx context.Context, tenantID, timelineID string) (bool, error) {
	return r.exists(ctx, r.config.StorageController, "/control/v1/tenant/"+tenantID+"/timeline/"+timelineID)
}

func (r *NeonRuntime) exists(ctx context.Context, target NeonControlTarget, path string) (bool, error) {
	_, status, err := r.request(ctx, target, http.MethodGet, path, nil)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("Neon existence check returned HTTP %d", status)
	}
}

func (r *NeonRuntime) doJSON(ctx context.Context, target NeonControlTarget, method, path string, body any, expected int) ([]byte, error) {
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	return r.doRawJSON(ctx, target, method, path, raw, expected)
}

func (r *NeonRuntime) doJSONOwned(ctx context.Context, target NeonControlTarget, method, path string, body any, ownershipToken string, expected int) ([]byte, error) {
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	return r.doRawJSONOwned(ctx, target, method, path, raw, ownershipToken, expected)
}

func (r *NeonRuntime) verifyOwnershipCapability(ctx context.Context) error {
	if r.config.allowUnqualifiedOwnershipProtocolForTest {
		return nil
	}
	body, status, err := r.request(ctx, r.config.StorageController, http.MethodGet, "/control/v1/hakopod/ownership", nil)
	if err != nil {
		return fmt.Errorf("verify Neon provider ownership protocol: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("verify Neon provider ownership protocol returned HTTP %d", status)
	}
	if err = verifyNeonOwnershipCapability(body); err != nil {
		return fmt.Errorf("verify Neon provider ownership protocol: %w", err)
	}
	return nil
}

func (r *NeonRuntime) doRawJSON(ctx context.Context, target NeonControlTarget, method, path string, body []byte, expected int) ([]byte, error) {
	response, status, err := r.request(ctx, target, method, path, body)
	if err != nil {
		return nil, err
	}
	if status != expected {
		return nil, fmt.Errorf("Neon %s %s returned HTTP %d", method, path, status)
	}
	return response, nil
}

func (r *NeonRuntime) doRawJSONOwned(ctx context.Context, target NeonControlTarget, method, path string, body []byte, ownershipToken string, expected int) ([]byte, error) {
	response, status, err := r.requestOwned(ctx, target, method, path, body, ownershipToken)
	if err != nil {
		return nil, err
	}
	if status != expected {
		return nil, fmt.Errorf("Neon %s %s returned HTTP %d", method, path, status)
	}
	return response, nil
}

func (r *NeonRuntime) request(ctx context.Context, target NeonControlTarget, method, path string, body []byte) ([]byte, int, error) {
	return r.requestWithOwnership(ctx, target, method, path, body, "")
}

func (r *NeonRuntime) requestOwned(ctx context.Context, target NeonControlTarget, method, path string, body []byte, ownershipToken string) ([]byte, int, error) {
	if !validNeonComputeOwnershipToken(ownershipToken) {
		return nil, 0, fmt.Errorf("Neon compute ownership token is invalid")
	}
	return r.requestWithOwnership(ctx, target, method, path, body, ownershipToken)
}

func (r *NeonRuntime) requestWithOwnership(ctx context.Context, target NeonControlTarget, method, path string, body []byte, ownershipToken string) ([]byte, int, error) {
	requestURI, err := url.ParseRequestURI(path)
	if err != nil || !strings.HasPrefix(requestURI.Path, "/") || requestURI.Fragment != "" {
		return nil, 0, fmt.Errorf("invalid Neon control path")
	}
	base, _ := url.Parse(target.Origin)
	endpoint := *base
	endpoint.Path = requestURI.Path
	endpoint.RawQuery = requestURI.RawQuery
	requestContext, cancel := context.WithTimeout(ctx, r.config.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+target.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ownershipToken != "" {
		req.Header.Set(neonOwnershipHeader, ownershipToken)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxNeonResponseBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(data) > maxNeonResponseBytes {
		return nil, 0, fmt.Errorf("Neon response exceeded %d bytes", maxNeonResponseBytes)
	}
	return data, resp.StatusCode, nil
}

func (r *NeonRuntime) rollback(ctx context.Context, request NeonLifecycleRequest, state *NeonLifecycleState) error {
	var failures []error
	computeNames := append([]string(nil), state.AttachedComputes...)
	for i := len(computeNames) - 1; i >= 0; i-- {
		target, ok := r.computeTarget(computeNames[i])
		if !ok {
			continue
		}
		_, status, requestErr := r.request(ctx, target, http.MethodPost, "/terminate?mode=immediate", nil)
		if requestErr != nil || status != http.StatusOK && status != http.StatusCreated {
			failures = append(failures, fmt.Errorf("terminate compute %s returned HTTP %d: %w", target.Name, status, requestErr))
		}
	}
	if state.TimelineCreated {
		_, status, requestErr := r.request(ctx, r.config.StorageController, http.MethodDelete, "/v1/tenant/"+request.TenantID+"/timeline/"+request.TimelineID, nil)
		if requestErr != nil || status != http.StatusOK && status != http.StatusNotFound {
			failures = append(failures, fmt.Errorf("delete timeline returned HTTP %d: %w", status, requestErr))
		}
	}
	if state.TenantCreated {
		_, status, requestErr := r.request(ctx, r.config.StorageController, http.MethodDelete, "/v1/tenant/"+request.TenantID, nil)
		if requestErr != nil || status != http.StatusOK && status != http.StatusNotFound {
			failures = append(failures, fmt.Errorf("delete tenant returned HTTP %d: %w", status, requestErr))
		}
	}
	if state.TenantRequested && !state.TenantCreated || state.TimelineRequested && !state.TimelineCreated || state.AttachingCompute != "" {
		state.CleanupRequired = true
		failures = append(failures, fmt.Errorf("remote ownership is ambiguous; destructive cleanup was not attempted"))
	}
	state.RolledBack = len(failures) == 0
	if state.RolledBack {
		state.AttachedComputes = nil
		state.AttachingCompute = ""
		state.TimelineCreated = false
		state.SafekeeperCount = 0
		state.SafekeeperHosts = nil
		state.TenantCreated = false
		state.TenantReady = false
		state.TenantRequested = false
		state.TimelineRequested = false
	}
	return errors.Join(failures...)
}

func (r *NeonRuntime) computeTarget(name string) (NeonControlTarget, bool) {
	for _, target := range r.config.Computes {
		if target.Name == name {
			return target, true
		}
	}
	return NeonControlTarget{}, false
}

func (r *NeonRuntime) statePath(operationID string) string {
	return filepath.Join(r.config.StateDirectory, operationID+".json")
}

func (r *NeonRuntime) lock(operationID string) (func(), error) {
	path := filepath.Join(r.config.StateDirectory, operationID+".lock")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("open Neon operation lock: %w", err)
	}
	unlock, err := lockNeonStateFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("Neon operation is already running: %w", err)
	}
	return func() { unlock(); _ = file.Close() }, nil
}

func (r *NeonRuntime) loadOrCreateState(request NeonLifecycleRequest, digest string) (NeonLifecycleState, error) {
	path := r.statePath(request.OperationID)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		state := NeonLifecycleState{SchemaVersion: 1, OperationID: request.OperationID, RequestDigest: digest, TenantID: request.TenantID, TimelineID: request.TimelineID}
		return state, r.storeState(state)
	}
	if err != nil {
		return NeonLifecycleState{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return NeonLifecycleState{}, fmt.Errorf("Neon lifecycle state must be a bounded private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return NeonLifecycleState{}, fmt.Errorf("read bounded Neon lifecycle state: %w", err)
	}
	var state NeonLifecycleState
	if err = json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("read Neon lifecycle state: %w", err)
	}
	if state.SchemaVersion != 1 || state.OperationID != request.OperationID || state.RequestDigest != digest || state.TenantID != request.TenantID || state.TimelineID != request.TimelineID {
		return state, fmt.Errorf("Neon operation state does not match the request")
	}
	return state, nil
}

func (r *NeonRuntime) storeState(state NeonLifecycleState) error {
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := r.statePath(state.OperationID)
	tmp, err := os.CreateTemp(r.config.StateDirectory, ".neon-state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(r.config.StateDirectory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 1024 {
		return value[:1024]
	}
	return value
}
