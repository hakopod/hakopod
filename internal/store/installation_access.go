package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
)

// AccessConfirmWindow is how long an unconfirmed access policy stays active.
const AccessConfirmWindow = 5 * time.Minute

// AccessPolicyRecord holds the policy as JSON so the store stays independent of
// its validation. Revision 0 means no policy was ever saved.
type AccessPolicyRecord struct {
	Revision     int64           `json:"revision"`
	Policy       json.RawMessage `json:"policy"`
	PendingUntil *time.Time      `json:"pending_until,omitempty"`
}

const accessColumns = "revision,policy,pending_until"

func scanAccess(row scanner) (AccessPolicyRecord, error) {
	var r AccessPolicyRecord
	err := row.Scan(&r.Revision, &r.Policy, &r.PendingUntil)
	return r, err
}

func accessAudit(ctx context.Context, tx pgx.Tx, identity, key, action string, metadata map[string]any) error {
	_, err := tx.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,$3,'installation-access',$4)", identity, key, action, JSON(metadata))
	return err
}

// AccessPolicy returns the active policy. An unconfirmed policy past its window
// is first restored to the previous one, in the same transaction as its audit.
func (s *Store) AccessPolicy(ctx context.Context) (AccessPolicyRecord, error) {
	current, err := scanAccess(s.Pool.QueryRow(ctx, "SELECT "+accessColumns+" FROM installation_access_policy WHERE singleton"))
	if errors.Is(err, pgx.ErrNoRows) {
		return AccessPolicyRecord{Policy: json.RawMessage(`{"enabled":false}`)}, nil
	}
	if err != nil || current.PendingUntil == nil || time.Now().Before(*current.PendingUntil) {
		return current, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return current, err
	}
	defer tx.Rollback(ctx)
	reverted, err := scanAccess(tx.QueryRow(ctx, `UPDATE installation_access_policy SET policy=previous,previous=NULL,pending_until=NULL,revision=revision+1,updated_at=now()
		WHERE singleton AND pending_until<=now() RETURNING `+accessColumns))
	if errors.Is(err, pgx.ErrNoRows) {
		// Another process confirmed or reverted it first.
		return scanAccess(s.Pool.QueryRow(ctx, "SELECT "+accessColumns+" FROM installation_access_policy WHERE singleton"))
	}
	if err != nil {
		return current, err
	}
	if err = accessAudit(ctx, tx, "system", "", "installation.access.reverted", map[string]any{"revision": reverted.Revision, "unconfirmed_revision": current.Revision}); err != nil {
		return current, err
	}
	return reverted, tx.Commit(ctx)
}

// PutAccessPolicy saves policy. Unless the new policy is off, the previous one
// is kept and restored after AccessConfirmWindow unless ConfirmAccessPolicy runs.
func (s *Store) PutAccessPolicy(ctx context.Context, p Principal, policy json.RawMessage, enabled bool, expected int64, metadata map[string]any) (AccessPolicyRecord, error) {
	if p.CredentialType != "browser" || !p.IsHuman() {
		return AccessPolicyRecord{}, ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return AccessPolicyRecord{}, err
	}
	defer tx.Rollback(ctx)
	// The lock also serializes first writes, before the singleton row exists.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044245)"); err != nil {
		return AccessPolicyRecord{}, err
	}
	var old json.RawMessage
	var revision int64
	err = tx.QueryRow(ctx, "SELECT revision,policy FROM installation_access_policy WHERE singleton FOR UPDATE").Scan(&revision, &old)
	if errors.Is(err, pgx.ErrNoRows) {
		old, err = json.RawMessage(`{"enabled":false}`), nil
	}
	if err != nil {
		return AccessPolicyRecord{}, err
	}
	if revision != expected {
		return AccessPolicyRecord{}, ErrConflict
	}
	var previous json.RawMessage
	var pending *time.Time
	if enabled {
		until := time.Now().Add(AccessConfirmWindow).UTC()
		previous, pending = old, &until
	}
	result, err := scanAccess(tx.QueryRow(ctx, `INSERT INTO installation_access_policy(singleton,revision,policy,previous,pending_until) VALUES(true,$1,$2,$3,$4)
		ON CONFLICT(singleton) DO UPDATE SET revision=excluded.revision,policy=excluded.policy,previous=excluded.previous,pending_until=excluded.pending_until,updated_at=now()
		RETURNING `+accessColumns, expected+1, policy, previous, pending))
	if err != nil {
		return AccessPolicyRecord{}, err
	}
	metadata["revision"], metadata["enabled"] = result.Revision, enabled
	if err = accessAudit(ctx, tx, p.ID, p.KeyID, "installation.access.updated", metadata); err != nil {
		return AccessPolicyRecord{}, err
	}
	return result, tx.Commit(ctx)
}

// ConfirmAccessPolicy keeps a pending policy. The caller has already been
// admitted by that policy, which is the proof that it does not lock them out.
func (s *Store) ConfirmAccessPolicy(ctx context.Context, p Principal, revision int64, metadata map[string]any) (AccessPolicyRecord, error) {
	if p.CredentialType != "browser" || !p.IsHuman() {
		return AccessPolicyRecord{}, ErrForbidden
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return AccessPolicyRecord{}, err
	}
	defer tx.Rollback(ctx)
	result, err := scanAccess(tx.QueryRow(ctx, `UPDATE installation_access_policy SET previous=NULL,pending_until=NULL,updated_at=now()
		WHERE singleton AND revision=$1 AND pending_until>now() RETURNING `+accessColumns, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return AccessPolicyRecord{}, ErrConflict
	}
	if err != nil {
		return AccessPolicyRecord{}, err
	}
	metadata["revision"] = revision
	if err = accessAudit(ctx, tx, p.ID, p.KeyID, "installation.access.confirmed", metadata); err != nil {
		return AccessPolicyRecord{}, err
	}
	return result, tx.Commit(ctx)
}

// ResetAccessPolicy is the host break-glass path. It runs as root on the
// server, never through the API, and works without a license.
func (s *Store) ResetAccessPolicy(ctx context.Context, actor string) (AccessPolicyRecord, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return AccessPolicyRecord{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044245)"); err != nil {
		return AccessPolicyRecord{}, err
	}
	result, err := scanAccess(tx.QueryRow(ctx, `INSERT INTO installation_access_policy(singleton,revision,policy) VALUES(true,1,'{"enabled":false}')
		ON CONFLICT(singleton) DO UPDATE SET revision=installation_access_policy.revision+1,policy=excluded.policy,previous=NULL,pending_until=NULL,updated_at=now()
		RETURNING `+accessColumns))
	if err != nil {
		return AccessPolicyRecord{}, err
	}
	if err = accessAudit(ctx, tx, "host", "", "installation.access.reset", map[string]any{"revision": result.Revision, "actor": actor}); err != nil {
		return AccessPolicyRecord{}, err
	}
	return result, tx.Commit(ctx)
}

// AccessVPNStatus is reported by the host timer, never by an HTTP caller.
type AccessVPNStatus struct {
	PublicKey       string     `json:"public_key"`
	ServerPublicKey string     `json:"server_public_key"`
	ServerError     string     `json:"server_error"`
	LatestHandshake *time.Time `json:"latest_handshake,omitempty"`
	RxBytes         int64      `json:"rx_bytes"`
	TxBytes         int64      `json:"tx_bytes"`
	Error           string     `json:"error"`
	ReportedAt      time.Time  `json:"reported_at"`
}

func (s *Store) AccessVPNStatus(ctx context.Context) (*AccessVPNStatus, error) {
	var v AccessVPNStatus
	err := s.Pool.QueryRow(ctx, "SELECT public_key,server_public_key,server_error,latest_handshake,rx_bytes,tx_bytes,error,reported_at FROM installation_access_vpn WHERE singleton").Scan(&v.PublicKey, &v.ServerPublicKey, &v.ServerError, &v.LatestHandshake, &v.RxBytes, &v.TxBytes, &v.Error, &v.ReportedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}

func (s *Store) ReportAccessVPN(ctx context.Context, v AccessVPNStatus) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO installation_access_vpn(singleton,public_key,latest_handshake,rx_bytes,tx_bytes,error,reported_at) VALUES(true,$1,$2,$3,$4,$5,now())
		ON CONFLICT(singleton) DO UPDATE SET public_key=excluded.public_key,latest_handshake=excluded.latest_handshake,rx_bytes=excluded.rx_bytes,tx_bytes=excluded.tx_bytes,error=excluded.error,reported_at=now()`,
		v.PublicKey, v.LatestHandshake, v.RxBytes, v.TxBytes, v.Error)
	return err
}

type AccessVPNPeer struct {
	ID              string     `json:"id"`
	IdentityID      string     `json:"identity_id"`
	Name            string     `json:"name"`
	PublicKey       string     `json:"public_key"`
	Address         string     `json:"address"`
	LatestHandshake *time.Time `json:"latest_handshake,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

const vpnPeerColumns = "id,identity_id,name,public_key,address,latest_handshake,created_at"

func (s *Store) AccessVPNPeers(ctx context.Context) ([]AccessVPNPeer, error) {
	rows, err := s.Pool.Query(ctx, "SELECT "+vpnPeerColumns+" FROM installation_access_vpn_peers WHERE revoked_at IS NULL ORDER BY created_at LIMIT 1024")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccessVPNPeer{}
	for rows.Next() {
		var peer AccessVPNPeer
		if err = rows.Scan(&peer.ID, &peer.IdentityID, &peer.Name, &peer.PublicKey, &peer.Address, &peer.LatestHandshake, &peer.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, peer)
	}
	return out, rows.Err()
}

// CreateAccessVPNPeer gives a device the lowest free address in network. The
// network address, the server's address and the broadcast address are never
// handed out.
func (s *Store) CreateAccessVPNPeer(ctx context.Context, p Principal, name, publicKey string, network netip.Prefix) (AccessVPNPeer, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return AccessVPNPeer{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(793044246)"); err != nil {
		return AccessVPNPeer{}, err
	}
	used := map[netip.Addr]bool{network.Addr(): true, network.Addr().Next(): true}
	rows, err := tx.Query(ctx, "SELECT address FROM installation_access_vpn_peers WHERE revoked_at IS NULL")
	if err != nil {
		return AccessVPNPeer{}, err
	}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return AccessVPNPeer{}, err
		}
		if address, err := netip.ParseAddr(raw); err == nil {
			used[address] = true
		}
	}
	rows.Close()
	var address netip.Addr
	for candidate := network.Addr(); network.Contains(candidate); candidate = candidate.Next() {
		if next := candidate.Next(); !used[candidate] && network.Contains(next) {
			address = candidate
			break
		}
	}
	if !address.IsValid() {
		return AccessVPNPeer{}, fmt.Errorf("%w: the VPN network has no free addresses", ErrConflict)
	}
	peer := AccessVPNPeer{ID: NewID(), IdentityID: p.ID, Name: name, PublicKey: publicKey, Address: address.String()}
	err = tx.QueryRow(ctx, "INSERT INTO installation_access_vpn_peers(id,identity_id,name,public_key,address) VALUES($1,$2,$3,$4,$5) RETURNING created_at", peer.ID, peer.IdentityID, peer.Name, peer.PublicKey, peer.Address).Scan(&peer.CreatedAt)
	if err != nil {
		return AccessVPNPeer{}, fmt.Errorf("%w: this public key already has a live profile", ErrConflict)
	}
	if err = accessAudit(ctx, tx, p.ID, p.KeyID, "installation.access.vpn_peer.created", map[string]any{"peer": peer.ID, "name": name, "address": peer.Address}); err != nil {
		return AccessVPNPeer{}, err
	}
	return peer, tx.Commit(ctx)
}

func (s *Store) RevokeAccessVPNPeer(ctx context.Context, p Principal, id string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, "UPDATE installation_access_vpn_peers SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if err = accessAudit(ctx, tx, p.ID, p.KeyID, "installation.access.vpn_peer.revoked", map[string]any{"peer": id}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReportAccessVPNServer is called by the host timer.
func (s *Store) ReportAccessVPNServer(ctx context.Context, publicKey, serverError string, handshakes map[string]time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO installation_access_vpn(singleton,server_public_key,server_error) VALUES(true,$1,$2)
		ON CONFLICT(singleton) DO UPDATE SET server_public_key=excluded.server_public_key,server_error=excluded.server_error,reported_at=now()`, publicKey, serverError); err != nil {
		return err
	}
	for key, at := range handshakes {
		if _, err = tx.Exec(ctx, "UPDATE installation_access_vpn_peers SET latest_handshake=$2 WHERE public_key=$1 AND revoked_at IS NULL", key, at); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
