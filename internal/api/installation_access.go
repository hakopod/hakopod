package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/access"
	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
)

// Signed provider webhooks stay reachable from any network. Their own
// signature checks are unchanged; nothing else is exempt.
var accessExemptWebhook = regexp.MustCompile(`^/api/v1/webhooks/(?:github|gitlab|git/[^/]+|github-app/[^/]+|nodes/[a-f0-9]{32}/git/[a-f0-9]{32})$`)

type accessState struct {
	mu     sync.Mutex
	at     time.Time
	record store.AccessPolicyRecord
	policy access.Policy
	local  []netip.Addr
}

func localAddresses() []netip.Addr {
	var out []netip.Addr
	addresses, _ := net.InterfaceAddrs()
	for _, address := range addresses {
		if prefix, err := netip.ParsePrefix(address.String()); err == nil {
			out = append(out, prefix.Addr().Unmap())
		}
	}
	return out
}

// accessPolicy is cached for five seconds. A read error is returned, never
// replaced by a stale or empty policy.
func (s *Server) accessPolicy(ctx context.Context) (access.Policy, store.AccessPolicyRecord, []netip.Addr, error) {
	state := &s.access
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.at.IsZero() && time.Since(state.at) < 5*time.Second {
		return state.policy, state.record, state.local, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	record, err := s.Store.AccessPolicy(ctx)
	if err != nil {
		state.at = time.Time{}
		return access.Policy{}, record, nil, err
	}
	var policy access.Policy
	if err = json.Unmarshal(record.Policy, &policy); err != nil {
		state.at = time.Time{}
		return access.Policy{}, record, nil, err
	}
	state.at, state.record, state.policy, state.local = time.Now(), record, policy, localAddresses()
	return policy, record, state.local, nil
}

func (s *Server) forgetAccessPolicy() {
	s.access.mu.Lock()
	s.access.at = time.Time{}
	s.access.mu.Unlock()
}

// accessPeer returns the connection peer and how to read proxy headers. The
// Node dashboard is the only loopback caller that names a peer; it forwards
// the socket address and the raw proxy headers under X-Hakopod-Peer-*. A
// loopback caller without that header is a local process on the server.
func accessPeer(r *http.Request) (peer netip.Addr, header func(string) string, viaDashboard bool, ok bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, nil, false, false
	}
	peer, err = netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, nil, false, false
	}
	peer = peer.Unmap()
	if !peer.IsLoopback() {
		return peer, r.Header.Get, false, true
	}
	named := r.Header.Get(access.PeerIPHeader)
	if named == "" {
		return peer, func(string) string { return "" }, false, true
	}
	peer, err = netip.ParseAddr(named)
	if err != nil {
		return netip.Addr{}, nil, true, false
	}
	return peer.Unmap(), func(name string) string { return r.Header.Get(access.PeerHeaderPrefix + name) }, true, true
}

// clientAddress applies policy to the request's peer. ok is false when the
// client cannot be determined, which every caller treats as a denial.
func clientAddress(r *http.Request, policy access.Policy) (netip.Addr, bool) {
	peer, header, _, ok := accessPeer(r)
	if !ok {
		return netip.Addr{}, false
	}
	return policy.ClientIP(peer, header)
}

// accessGate admits a request under the installation access policy. It runs
// before routing, so a denied network never reaches login, device-code or MFA.
func (s *Server) accessGate(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/internal/access/check" || (r.Method == http.MethodPost && accessExemptWebhook.MatchString(r.URL.Path)) {
		return true
	}
	if s.Store == nil || s.Auth.DeploymentMode == cluster.DeploymentManagedCloud {
		return true
	}
	policy, _, local, err := s.accessPolicy(r.Context())
	if err != nil {
		problem(w, 503, "access_policy_unavailable", "the installation access policy could not be read; run `hakopod-server access-reset` on the server with the API environment (see docs/management-network-access.md) if this persists")
		return false
	}
	client, ok := clientAddress(r, policy)
	if !ok || !policy.Allows(client, local) {
		problem(w, 403, "network_not_allowed", "this network is not allowed to reach the installation")
		return false
	}
	return true
}

// accessCheck lets the Node dashboard ask for a decision before it renders
// anything. Only loopback callers may use it.
func (s *Server) accessCheck(w http.ResponseWriter, r *http.Request) {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil || !isLoopback(host) {
		problem(w, 404, "not_found", "resource not found")
		return
	}
	if s.Store == nil || s.Auth.DeploymentMode == cluster.DeploymentManagedCloud {
		w.WriteHeader(204)
		return
	}
	policy, _, local, err := s.accessPolicy(r.Context())
	if err != nil {
		problem(w, 503, "access_policy_unavailable", "the installation access policy could not be read")
		return
	}
	client, ok := clientAddress(r, policy)
	if !ok || !policy.Allows(client, local) {
		problem(w, 403, "network_not_allowed", "this network is not allowed to reach the installation")
		return
	}
	w.WriteHeader(204)
}

func isLoopback(host string) bool {
	address, err := netip.ParseAddr(host)
	return err == nil && address.Unmap().IsLoopback()
}

type accessPolicyView struct {
	store.AccessPolicyRecord
	SecretsSet map[string]bool        `json:"connector_secrets_set,omitempty"`
	Connector  json.RawMessage        `json:"connector_status,omitempty"`
	Agent      *store.AccessAgent     `json:"agent,omitempty"`
	ClientIP   string                 `json:"client_ip"`
	Licensed   bool                   `json:"licensed"`
	VPNStatus  *store.AccessVPNStatus `json:"vpn_status,omitempty"`
}

func (s *Server) registerAccessRoutes(public, protected *http.ServeMux) {
	public.HandleFunc("GET /internal/access/check", s.accessCheck)
	protected.HandleFunc("GET /api/v1/installation/access", s.installationAccess)
	protected.HandleFunc("PUT /api/v1/installation/access", s.putInstallationAccess)
	protected.HandleFunc("POST /api/v1/installation/access/confirm", s.confirmInstallationAccess)
	protected.HandleFunc("GET /api/v1/installation/access/vpn-peers", s.listVPNPeers)
	protected.HandleFunc("POST /api/v1/installation/access/vpn-peers", s.createVPNPeer)
	protected.HandleFunc("DELETE /api/v1/installation/access/vpn-peers/{id}", s.revokeVPNPeer)
}

// Changing who can reach the installation needs an owner in a browser session:
// automation keys, CLI tokens and agent keys are refused.
func accessOwner(w http.ResponseWriter, r *http.Request, s *Server) bool {
	p := who(r)
	if s.Auth.DeploymentMode == cluster.DeploymentManagedCloud || !installationOwnerAuthority(p) || p.CredentialType != "browser" || !p.IsHuman() {
		failure(w, store.ErrForbidden)
		return false
	}
	return true
}

func (s *Server) installationAccess(w http.ResponseWriter, r *http.Request) {
	if s.Auth.DeploymentMode == cluster.DeploymentManagedCloud || !installationAdministrator(who(r)) {
		failure(w, store.ErrForbidden)
		return
	}
	s.forgetAccessPolicy()
	policy, record, _, err := s.accessPolicy(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	client, _ := clientAddress(r, policy)
	view := accessPolicyView{AccessPolicyRecord: record, Licensed: s.Store.RequireFeatures(r.Context(), "private_access") == nil}
	if view.VPNStatus, err = s.Store.AccessVPNStatus(r.Context()); err != nil {
		failure(w, err)
		return
	}
	agent, err := s.Store.AccessAgentStatus(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	view.Agent = &agent
	if view.Connector, err = s.Store.AccessConnectorStatus(r.Context()); err != nil {
		failure(w, err)
		return
	}
	if current, err := s.openConnectorSecrets(r.Context()); err == nil {
		view.SecretsSet = map[string]bool{"tailscale_auth_key": current.TailscaleAuthKey != "", "openvpn_profile": current.OpenVPNProfile != "", "ipsec_psk": current.IPsecPSK != ""}
	}
	if client.IsValid() {
		view.ClientIP = client.String()
	}
	write(w, 200, view)
}

func (s *Server) putInstallationAccess(w http.ResponseWriter, r *http.Request) {
	if !accessOwner(w, r, s) {
		return
	}
	var body struct {
		ExpectedRevision     int64         `json:"expected_revision"`
		Policy               access.Policy `json:"policy"`
		ConfirmFromElsewhere bool          `json:"confirm_from_elsewhere"`
		ConnectorSecrets     AccessSecrets `json:"connector_secrets"`
	}
	if !decodeLimited(w, r, &body, 64<<10) {
		return
	}
	policy, err := body.Policy.Normalize()
	if err != nil {
		problem(w, 400, "invalid_request", err.Error())
		return
	}
	if policy.Enabled || policy.VPN != nil || policy.VPNServer != nil {
		if err = s.Store.RequireFeatures(r.Context(), "private_access"); err != nil {
			authFailure(w, err)
			return
		}
	}
	// vpn_only is safe only once the tunnel is up: require a recent handshake.
	if policy.VPNOnly && policy.VPNServer == nil {
		status, err := s.Store.AccessVPNStatus(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		if status == nil || status.LatestHandshake == nil || time.Since(*status.LatestHandshake) > 2*time.Minute {
			problem(w, 409, "vpn_not_connected", "turn on vpn_only after the WireGuard connector has a handshake from the last two minutes")
			return
		}
	}
	// Refuse a save that would deny the network the owner is using now,
	// unless they say they will confirm it from another allowed network.
	client, ok := clientAddress(r, policy)
	if !body.ConfirmFromElsewhere && (!ok || !policy.Allows(client, localAddresses())) {
		problem(w, 409, "would_lock_out", "this policy would block the network you are using now; add it, or confirm from an allowed network within five minutes")
		return
	}
	raw, _ := json.Marshal(policy)
	view := accessPolicyView{Licensed: true}
	if ok {
		view.ClientIP = client.String()
	}
	metadata := map[string]any{"allow_cidrs": len(policy.AllowCIDRs), "client_ip_source": policy.ClientIPSource, "client_ip": view.ClientIP}
	sealed, err := s.connectorSecrets(r.Context(), policy, body.ConnectorSecrets)
	if err != nil {
		authFailure(w, err)
		return
	}
	view.AccessPolicyRecord, err = s.Store.PutAccessPolicy(r.Context(), who(r), raw, sealed, policy.Enabled, body.ExpectedRevision, metadata)
	s.forgetAccessPolicy()
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, view)
}

func (s *Server) confirmInstallationAccess(w http.ResponseWriter, r *http.Request) {
	if !accessOwner(w, r, s) {
		return
	}
	var body struct {
		Revision int64 `json:"revision"`
	}
	if !decodeLimited(w, r, &body, 4<<10) {
		return
	}
	// accessGate already admitted this request under the pending policy.
	policy, _, _, err := s.accessPolicy(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	client, _ := clientAddress(r, policy)
	record, err := s.Store.ConfirmAccessPolicy(r.Context(), who(r), body.Revision, map[string]any{"client_ip": client.String()})
	s.forgetAccessPolicy()
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, accessPolicyView{AccessPolicyRecord: record, ClientIP: client.String(), Licensed: true})
}

// requestIP keys rate limits by the dashboard's real peer rather than by
// loopback, so one noisy client no longer shares a bucket with everyone.
func requestIP(r *http.Request) string {
	if peer, _, _, ok := accessPeer(r); ok {
		return peer.String()
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

type vpnPeerProfile struct {
	store.AccessVPNPeer
	// What the browser needs to write the device's WireGuard profile. The
	// device's private key never reaches the server.
	ServerPublicKey string `json:"server_public_key"`
	Endpoint        string `json:"endpoint"`
	ServerAddress   string `json:"server_address"`
	Network         string `json:"network"`
}

// VPN profiles are for installation administrators in a browser session.
func vpnPeerAdministrator(w http.ResponseWriter, r *http.Request, s *Server) bool {
	p := who(r)
	if s.Auth.DeploymentMode == cluster.DeploymentManagedCloud || !installationAdministrator(p) || p.CredentialType != "browser" || !p.IsHuman() {
		failure(w, store.ErrForbidden)
		return false
	}
	return true
}

func (s *Server) vpnServer(w http.ResponseWriter, r *http.Request) (*access.VPNServer, string, bool) {
	s.forgetAccessPolicy()
	policy, _, _, err := s.accessPolicy(r.Context())
	if err != nil {
		failure(w, err)
		return nil, "", false
	}
	if policy.VPNServer == nil {
		problem(w, 409, "vpn_server_off", "turn on the Hakopod VPN server before creating profiles")
		return nil, "", false
	}
	status, err := s.Store.AccessVPNStatus(r.Context())
	if err != nil {
		failure(w, err)
		return nil, "", false
	}
	key := ""
	if status != nil {
		key = status.ServerPublicKey
	}
	return policy.VPNServer, key, true
}

func (s *Server) listVPNPeers(w http.ResponseWriter, r *http.Request) {
	if !vpnPeerAdministrator(w, r, s) {
		return
	}
	peers, err := s.Store.AccessVPNPeers(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": peers})
}

func (s *Server) createVPNPeer(w http.ResponseWriter, r *http.Request) {
	if !vpnPeerAdministrator(w, r, s) {
		return
	}
	var body struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}
	if !decodeLimited(w, r, &body, 4<<10) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body.PublicKey))
	if len(body.Name) < 1 || len(body.Name) > 64 || err != nil || len(key) != 32 {
		problem(w, 400, "invalid_request", "send a device name of 1–64 characters and a WireGuard public key")
		return
	}
	if err = s.Store.RequireFeatures(r.Context(), "private_access"); err != nil {
		authFailure(w, err)
		return
	}
	server, serverKey, ok := s.vpnServer(w, r)
	if !ok {
		return
	}
	if serverKey == "" {
		problem(w, 409, "vpn_server_starting", "the server has not reported its WireGuard key yet; check that the private-access module is installed and retry")
		return
	}
	peer, err := s.Store.CreateAccessVPNPeer(r.Context(), who(r), body.Name, base64.StdEncoding.EncodeToString(key), netip.MustParsePrefix(server.Network))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 201, vpnPeerProfile{AccessVPNPeer: peer, ServerPublicKey: serverKey, Endpoint: server.Endpoint, ServerAddress: server.Address().Addr().String(), Network: server.Network})
}

func (s *Server) revokeVPNPeer(w http.ResponseWriter, r *http.Request) {
	if !vpnPeerAdministrator(w, r, s) {
		return
	}
	if err := s.Store.RevokeAccessVPNPeer(r.Context(), who(r), r.PathValue("id")); err != nil {
		failure(w, err)
		return
	}
	w.WriteHeader(204)
}

// AccessSecrets are write-only: the API accepts them and the host timer reads
// them, but no response ever contains them.
type AccessSecrets struct {
	TailscaleAuthKey string `json:"tailscale_auth_key,omitempty"`
	OpenVPNProfile   string `json:"openvpn_profile,omitempty"`
	IPsecPSK         string `json:"ipsec_psk,omitempty"`
}

func (s *Server) openConnectorSecrets(ctx context.Context) (AccessSecrets, error) {
	var out AccessSecrets
	sealed, err := s.Store.AccessConnectorSecrets(ctx)
	if err != nil || len(sealed) == 0 {
		return out, err
	}
	plain, err := s.decryptAuth(sealed)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(plain, &out)
}

// OpenAccessSecrets lets the host-side state command read the secrets with
// the installation's authentication key.
func OpenAccessSecrets(encryptionKey string, sealed []byte) (AccessSecrets, error) {
	var out AccessSecrets
	if len(sealed) == 0 {
		return out, nil
	}
	plain, err := (&Server{Auth: AuthConfig{EncryptionKey: encryptionKey}}).decryptAuth(sealed)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(plain, &out)
}

// connectorSecrets merges new secrets over the stored ones, drops secrets for
// connectors that are no longer configured, checks what each connector needs
// and returns the sealed result.
func (s *Server) connectorSecrets(ctx context.Context, policy access.Policy, in AccessSecrets) ([]byte, error) {
	current, err := s.openConnectorSecrets(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: stored connector secrets could not be read; check the installation authentication key", store.ErrInput)
	}
	for _, pair := range []struct{ next, old *string }{{&in.TailscaleAuthKey, &current.TailscaleAuthKey}, {&in.OpenVPNProfile, &current.OpenVPNProfile}, {&in.IPsecPSK, &current.IPsecPSK}} {
		if *pair.next != "" {
			*pair.old = *pair.next
		}
	}
	if policy.Tailscale == nil {
		current.TailscaleAuthKey = ""
	} else if len(current.TailscaleAuthKey) < 16 || len(current.TailscaleAuthKey) > 512 || strings.ContainsAny(current.TailscaleAuthKey, " \n\t") {
		return nil, fmt.Errorf("%w: Tailscale needs an auth key", store.ErrInput)
	}
	if policy.OpenVPN == nil {
		current.OpenVPNProfile = ""
	} else if err = access.ValidateOpenVPNProfile(current.OpenVPNProfile); err != nil {
		return nil, fmt.Errorf("%w: %s", store.ErrInput, err.Error())
	}
	if policy.IPsec == nil {
		current.IPsecPSK = ""
	} else if len(current.IPsecPSK) < 20 || len(current.IPsecPSK) > 256 || strings.ContainsAny(current.IPsecPSK, "\"\n\r") {
		return nil, fmt.Errorf("%w: IPsec needs a pre-shared key of 20 to 256 characters without quotes or line breaks", store.ErrInput)
	}
	if current == (AccessSecrets{}) {
		return []byte{}, nil
	}
	return s.encryptAuth(store.JSON(current))
}
