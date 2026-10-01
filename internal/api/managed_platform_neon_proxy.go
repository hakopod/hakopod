package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

const maxNeonProxyRequestBytes = 8 << 10

var neonProxySessionID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var neonProxyName = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$`)
var neonSCRAMSecret = regexp.MustCompile(`^SCRAM-SHA-256\$[1-9][0-9]{3,8}:[A-Za-z0-9+/]+=*\$[A-Za-z0-9+/]+=*:[A-Za-z0-9+/]+=*$`)

type NeonProxyAccess struct {
	RoleSecret             string         `json:"role_secret"`
	ProjectID              string         `json:"project_id,omitempty"`
	AccountID              string         `json:"account_id,omitempty"`
	AllowedIPs             []string       `json:"allowed_ips"`
	AllowedVPCEndpointIDs  []string       `json:"allowed_vpc_endpoint_ids"`
	BlockPublicConnections bool           `json:"block_public_connections"`
	BlockVPCConnections    bool           `json:"block_vpc_connections"`
	RateLimits             map[string]any `json:"rate_limits,omitempty"`
}

type NeonProxyRoute struct {
	Address    string `json:"address"`
	ServerName string `json:"server_name"`
	EndpointID string `json:"endpoint_id"`
	ProjectID  string `json:"project_id"`
	BranchID   string `json:"branch_id"`
	ComputeID  string `json:"compute_id"`
}

type NeonProxyRoleConfig struct {
	SCRAMSecret            string   `toml:"scram_secret"`
	AllowedIPs             []string `toml:"allowed_ips"`
	AllowedVPCEndpointIDs  []string `toml:"allowed_vpc_endpoint_ids"`
	BlockPublicConnections bool     `toml:"block_public_connections"`
	BlockVPCConnections    bool     `toml:"block_vpc_connections"`
}

type NeonProxyEndpointConfig struct {
	Enabled    bool                           `toml:"enabled"`
	Address    string                         `toml:"address"`
	ServerName string                         `toml:"server_name"`
	ProjectID  string                         `toml:"project_id"`
	BranchID   string                         `toml:"branch_id"`
	ComputeID  string                         `toml:"compute_id"`
	Roles      map[string]NeonProxyRoleConfig `toml:"roles"`
}

type NeonProxyBootstrapConfig struct {
	Enabled bool                           `toml:"enabled"`
	Roles   map[string]NeonProxyRoleConfig `toml:"roles"`
}

func ValidateNeonProxyBootstrap(config NeonProxyBootstrapConfig) error {
	if !config.Enabled {
		return fmt.Errorf("Neon proxy bootstrap must be enabled")
	}
	_, err := NewConfiguredNeonProxyAuthority(strings.Repeat("x", 32), map[string]NeonProxyEndpointConfig{
		"bootstrap": {
			Enabled:    true,
			Address:    "neon-compute-0.managed-platform-bootstrap.svc:55433",
			ServerName: "neon-compute-0.managed-platform-bootstrap.svc",
			ProjectID:  "bootstrap",
			BranchID:   "bootstrap",
			ComputeID:  "compute-0",
			Roles:      config.Roles,
		},
	})
	return err
}

type ConfiguredNeonProxyAuthority struct {
	token     string
	endpoints map[string]NeonProxyEndpointConfig
}

type NeonProxyEndpointStore interface {
	NeonProxyEndpoint(context.Context, string) (store.NeonProxyEndpointRecord, error)
}

type DatabaseNeonProxyAuthority struct {
	Store         NeonProxyEndpointStore
	Token         string
	EncryptionKey []byte
}

func NewDatabaseNeonProxyAuthority(database NeonProxyEndpointStore, token string, encryptionKey []byte) (*DatabaseNeonProxyAuthority, error) {
	if database == nil || len(token) < 32 || len(token) > 8192 || len(encryptionKey) != 32 {
		return nil, fmt.Errorf("durable Neon proxy authority configuration is invalid")
	}
	return &DatabaseNeonProxyAuthority{Store: database, Token: token, EncryptionKey: append([]byte(nil), encryptionKey...)}, nil
}

func (a *DatabaseNeonProxyAuthority) ValidNeonProxyToken(value string) bool {
	return a != nil && ConstantTimeNeonProxyToken(a.Token, value)
}

func (a *DatabaseNeonProxyAuthority) endpoint(ctx context.Context, endpoint string) (NeonProxyEndpointConfig, error) {
	if a == nil || a.Store == nil {
		return NeonProxyEndpointConfig{}, fmt.Errorf("unavailable")
	}
	record, err := a.Store.NeonProxyEndpoint(ctx, endpoint)
	if err != nil {
		return NeonProxyEndpointConfig{}, fmt.Errorf("unavailable")
	}
	if record.EndpointID != endpoint || record.EndpointID != record.PlatformID || record.ProjectID != record.PlatformID || record.PlatformRevision < 1 || record.Generation != record.PlatformRevision || !record.Enabled {
		return NeonProxyEndpointConfig{}, fmt.Errorf("unavailable")
	}
	sealed, err := managedplatform.OpenNeonProxyRoles(a.EncryptionKey, record.PlatformID, record.PlatformRevision, record.EndpointID, record.EncryptedRoles)
	if err != nil {
		return NeonProxyEndpointConfig{}, fmt.Errorf("unavailable")
	}
	roles := make(map[string]NeonProxyRoleConfig, len(sealed))
	for name, value := range sealed {
		roles[name] = NeonProxyRoleConfig{SCRAMSecret: value.SCRAMSecret, AllowedIPs: append([]string(nil), value.AllowedIPs...), AllowedVPCEndpointIDs: append([]string(nil), value.AllowedVPCEndpointIDs...), BlockPublicConnections: value.BlockPublicConnections, BlockVPCConnections: value.BlockVPCConnections}
	}
	config := NeonProxyEndpointConfig{Enabled: record.Enabled, Address: record.Address, ServerName: record.ServerName, ProjectID: record.ProjectID, BranchID: record.BranchID, ComputeID: record.ComputeID, Roles: roles}
	validator, err := NewConfiguredNeonProxyAuthority(strings.Repeat("x", 32), map[string]NeonProxyEndpointConfig{endpoint: config})
	if err != nil {
		return NeonProxyEndpointConfig{}, fmt.Errorf("unavailable")
	}
	return validator.endpoints[endpoint], nil
}

func (a *DatabaseNeonProxyAuthority) AuthorizeNeonProxy(ctx context.Context, endpoint, role string) (NeonProxyAccess, error) {
	config, err := a.endpoint(ctx, endpoint)
	if err != nil {
		return NeonProxyAccess{}, err
	}
	access, ok := config.Roles[role]
	if !ok {
		return NeonProxyAccess{}, fmt.Errorf("unavailable")
	}
	return NeonProxyAccess{RoleSecret: access.SCRAMSecret, ProjectID: config.ProjectID, AllowedIPs: append([]string(nil), access.AllowedIPs...), AllowedVPCEndpointIDs: append([]string(nil), access.AllowedVPCEndpointIDs...), BlockPublicConnections: access.BlockPublicConnections, BlockVPCConnections: access.BlockVPCConnections}, nil
}
func (a *DatabaseNeonProxyAuthority) WakeNeonCompute(ctx context.Context, endpoint string) (NeonProxyRoute, error) {
	config, err := a.endpoint(ctx, endpoint)
	if err != nil {
		return NeonProxyRoute{}, err
	}
	return NeonProxyRoute{Address: config.Address, ServerName: config.ServerName, EndpointID: endpoint, ProjectID: config.ProjectID, BranchID: config.BranchID, ComputeID: config.ComputeID}, nil
}

func NewConfiguredNeonProxyAuthority(token string, endpoints map[string]NeonProxyEndpointConfig) (*ConfiguredNeonProxyAuthority, error) {
	if len(token) < 32 || len(token) > 8192 || len(endpoints) == 0 || len(endpoints) > 64 {
		return nil, fmt.Errorf("Neon proxy authority configuration is invalid")
	}
	copyEndpoints := make(map[string]NeonProxyEndpointConfig, len(endpoints))
	for endpoint, config := range endpoints {
		if !neonProxyName.MatchString(endpoint) || !neonProxyName.MatchString(config.ProjectID) || !neonProxyName.MatchString(config.BranchID) || !neonProxyName.MatchString(config.ComputeID) || len(config.Roles) == 0 || len(config.Roles) > 64 {
			return nil, fmt.Errorf("Neon proxy endpoint configuration is invalid")
		}
		host, port, err := net.SplitHostPort(config.Address)
		number, numberErr := strconv.Atoi(port)
		if err != nil || host == "" || numberErr != nil || number < 1 || number > 65535 || net.ParseIP(host) != nil || !strings.HasSuffix(host, ".svc") || len(config.ServerName) > 253 || config.ServerName != host {
			return nil, fmt.Errorf("Neon proxy endpoint route is invalid")
		}
		roles := make(map[string]NeonProxyRoleConfig, len(config.Roles))
		for role, value := range config.Roles {
			if !neonProxyName.MatchString(role) || !neonSCRAMSecret.MatchString(value.SCRAMSecret) || len(value.AllowedIPs) > 64 || len(value.AllowedVPCEndpointIDs) > 64 {
				return nil, fmt.Errorf("Neon proxy role configuration is invalid")
			}
			ips := append([]string(nil), value.AllowedIPs...)
			for _, pattern := range ips {
				if ip := net.ParseIP(pattern); ip == nil {
					if _, network, parseErr := net.ParseCIDR(pattern); parseErr != nil || network.String() != pattern {
						return nil, fmt.Errorf("Neon proxy role network policy is invalid")
					}
				}
			}
			sort.Strings(ips)
			value.AllowedIPs = ips
			value.AllowedVPCEndpointIDs = append([]string(nil), value.AllowedVPCEndpointIDs...)
			sort.Strings(value.AllowedVPCEndpointIDs)
			roles[role] = value
		}
		config.Roles = roles
		copyEndpoints[endpoint] = config
	}
	return &ConfiguredNeonProxyAuthority{token: token, endpoints: copyEndpoints}, nil
}

func (a *ConfiguredNeonProxyAuthority) ValidNeonProxyToken(value string) bool {
	return a != nil && ConstantTimeNeonProxyToken(a.token, value)
}
func (a *ConfiguredNeonProxyAuthority) AuthorizeNeonProxy(_ context.Context, endpoint, role string) (NeonProxyAccess, error) {
	if a == nil {
		return NeonProxyAccess{}, fmt.Errorf("unavailable")
	}
	config, ok := a.endpoints[endpoint]
	if !ok || !config.Enabled {
		return NeonProxyAccess{}, fmt.Errorf("unavailable")
	}
	access, ok := config.Roles[role]
	if !ok {
		return NeonProxyAccess{}, fmt.Errorf("unavailable")
	}
	return NeonProxyAccess{RoleSecret: access.SCRAMSecret, ProjectID: config.ProjectID, AllowedIPs: append([]string(nil), access.AllowedIPs...), AllowedVPCEndpointIDs: append([]string(nil), access.AllowedVPCEndpointIDs...), BlockPublicConnections: access.BlockPublicConnections, BlockVPCConnections: access.BlockVPCConnections}, nil
}
func (a *ConfiguredNeonProxyAuthority) WakeNeonCompute(_ context.Context, endpoint string) (NeonProxyRoute, error) {
	if a == nil {
		return NeonProxyRoute{}, fmt.Errorf("unavailable")
	}
	config, ok := a.endpoints[endpoint]
	if !ok || !config.Enabled {
		return NeonProxyRoute{}, fmt.Errorf("unavailable")
	}
	return NeonProxyRoute{Address: config.Address, ServerName: config.ServerName, EndpointID: endpoint, ProjectID: config.ProjectID, BranchID: config.BranchID, ComputeID: config.ComputeID}, nil
}

// NeonProxyAuthority is the private, server-owned authorization and routing
// boundary used by the pinned Neon proxy. Implementations must read durable
// current state and must not accept routes or role secrets from HTTP callers.
type NeonProxyAuthority interface {
	AuthorizeNeonProxy(context.Context, string, string) (NeonProxyAccess, error)
	WakeNeonCompute(context.Context, string) (NeonProxyRoute, error)
	ValidNeonProxyToken(string) bool
}

func (s *Server) registerNeonProxyControlPlane(mux *http.ServeMux) {
	if s.NeonProxyAuthority == nil {
		return
	}
	mux.HandleFunc("GET /api/v1/internal/neon/proxy/get_endpoint_access_control", s.neonProxyAccess)
	mux.HandleFunc("GET /api/v1/internal/neon/proxy/wake_compute", s.neonProxyWake)
}

func (s *Server) neonProxyAccess(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeNeonProxy(w, r) {
		return
	}
	if !s.acquireNeonProxy(w) {
		return
	}
	defer func() { <-s.neonProxyConcurrent }()
	endpoint, role, ok := neonProxyQuery(w, r, true)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	access, err := s.NeonProxyAuthority.AuthorizeNeonProxy(ctx, endpoint, role)
	if err != nil {
		neonProxyFailure(w, err)
		return
	}
	write(w, http.StatusOK, access)
}

func (s *Server) neonProxyWake(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeNeonProxy(w, r) {
		return
	}
	if !s.acquireNeonProxy(w) {
		return
	}
	defer func() { <-s.neonProxyConcurrent }()
	endpoint, _, ok := neonProxyQuery(w, r, false)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	route, err := s.NeonProxyAuthority.WakeNeonCompute(ctx, endpoint)
	if err != nil {
		neonProxyFailure(w, err)
		return
	}
	if _, _, err = net.SplitHostPort(route.Address); err != nil || route.ServerName == "" || route.EndpointID != endpoint || route.ProjectID == "" || route.BranchID == "" || route.ComputeID == "" {
		neonProxyFailure(w, fmt.Errorf("invalid durable Neon route"))
		return
	}
	write(w, http.StatusOK, map[string]any{"address": route.Address, "server_name": route.ServerName, "aux": map[string]string{"endpoint_id": route.EndpointID, "project_id": route.ProjectID, "branch_id": route.BranchID, "compute_id": route.ComputeID, "cold_start_info": "warm"}})
}

func (s *Server) acquireNeonProxy(w http.ResponseWriter) bool {
	select {
	case s.neonProxyConcurrent <- struct{}{}:
		return true
	default:
		neonProxyProblem(w, http.StatusTooManyRequests, "proxy control plane is busy")
		return false
	}
}

func (s *Server) authorizeNeonProxy(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength > maxNeonProxyRequestBytes {
		neonProxyProblem(w, http.StatusRequestEntityTooLarge, "request rejected")
		return false
	}
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") || len(value) <= len("Bearer ") || !s.NeonProxyAuthority.ValidNeonProxyToken(value[len("Bearer "):]) {
		neonProxyProblem(w, http.StatusUnauthorized, "proxy authorization failed")
		return false
	}
	requestID := r.Header.Get("X-Request-ID")
	if !neonProxySessionID.MatchString(requestID) || r.URL.Query().Get("session_id") != requestID {
		neonProxyProblem(w, http.StatusBadRequest, "invalid proxy request")
		return false
	}
	return true
}

func neonProxyQuery(w http.ResponseWriter, r *http.Request, requireRole bool) (string, string, bool) {
	query := r.URL.Query()
	endpoint, role := query.Get("endpointish"), query.Get("role")
	if endpoint == "" || len(endpoint) > 63 || len(role) > 63 || requireRole && role == "" || len(query.Encode()) > maxNeonProxyRequestBytes {
		neonProxyProblem(w, http.StatusBadRequest, "invalid proxy request")
		return "", "", false
	}
	return endpoint, role, true
}

func neonProxyFailure(w http.ResponseWriter, _ error) {
	// The pinned proxy logs non-2xx response bodies. Keep the response generic:
	// implementation errors may contain addresses, role names, or credentials.
	neonProxyProblem(w, http.StatusServiceUnavailable, "endpoint is unavailable")
}

func neonProxyProblem(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message, "status": map[string]any{"code": "PROJECT_UNDER_MAINTENANCE", "message": message, "details": "request could not be completed"}})
}

func ConstantTimeNeonProxyToken(expected, presented string) bool {
	return len(expected) >= 32 && len(expected) <= 8192 && len(presented) == len(expected) && subtle.ConstantTimeCompare([]byte(expected), []byte(presented)) == 1
}
