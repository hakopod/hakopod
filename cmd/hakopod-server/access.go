package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/hakopod/hakopod/internal/access"
	"github.com/hakopod/hakopod/internal/store"
)

// runAccessReset is the break-glass path for a management access policy that
// locks every administrator out. It runs on the server with the API's own
// settings, so it needs no network path to the dashboard and no license.
// It ships with every release binary, unlike the maintenance helper.
func runAccessReset() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := accessStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	actor := os.Getenv("HAKOPOD_ACCESS_RESET_ACTOR")
	if actor == "" {
		actor = "host"
	}
	record, err := db.ResetAccessPolicy(ctx, actor)
	if err != nil {
		return fmt.Errorf("reset the access policy: %w", err)
	}
	fmt.Printf("Management access policy is off (revision %d). Every network can reach the dashboard again.\n", record.Revision)
	return nil
}

func accessStore(ctx context.Context) (*store.Store, error) {
	if err := loadOperatorConfig(); err != nil {
		return nil, err
	}
	dbURL, err := secretSetting("HAKOPOD_DATABASE_URL")
	if err != nil || dbURL == "" {
		return nil, fmt.Errorf("HAKOPOD_DATABASE_URL or HAKOPOD_DATABASE_URL_FILE is required; run with the API environment")
	}
	db, err := store.Open(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("connect to PostgreSQL: check HAKOPOD_DATABASE_URL and database health")
	}
	return db, nil
}

// runAccessFirewallState prints what the host firewall should enforce. The
// private-access module's root timer runs it as the API user and applies the
// result with nftables. Reading the policy also restores an unconfirmed policy
// whose window has passed, so the firewall reverts with the API.
func runAccessFirewallState() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := accessStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	record, err := db.AccessPolicy(ctx)
	if err != nil {
		return fmt.Errorf("read the access policy: %w", err)
	}
	var policy access.Policy
	if err = json.Unmarshal(record.Policy, &policy); err != nil {
		return fmt.Errorf("the stored access policy is invalid")
	}
	type firewall struct {
		Enabled    bool     `json:"enabled"`
		AllowCIDRs []string `json:"allow_cidrs"`
		TunnelOnly bool     `json:"tunnel_only"`
	}
	type peer struct {
		PublicKey string `json:"public_key"`
		Address   string `json:"address"`
	}
	type server struct {
		ListenPort int    `json:"listen_port"`
		Address    string `json:"address"`
		Peers      []peer `json:"peers"`
	}
	state := struct {
		Firewall  firewall    `json:"firewall"`
		VPN       *access.VPN `json:"vpn"`
		VPNServer *server     `json:"vpn_server"`
	}{Firewall: firewall{policy.Enabled && policy.Firewall, []string{}, policy.VPNOnly}, VPN: policy.VPN}
	if policy.VPNServer != nil {
		peers, err := db.AccessVPNPeers(ctx)
		if err != nil {
			return fmt.Errorf("read VPN peers: %w", err)
		}
		state.VPNServer = &server{ListenPort: policy.VPNServer.ListenPort, Address: policy.VPNServer.Address().String(), Peers: []peer{}}
		for _, p := range peers {
			state.VPNServer.Peers = append(state.VPNServer.Peers, peer{p.PublicKey, p.Address})
		}
	}
	if state.Firewall.Enabled {
		state.Firewall.AllowCIDRs = policy.AllowCIDRs
	}
	return json.NewEncoder(os.Stdout).Encode(state)
}

// runAccessVPNReport stores what the host timer observed about the WireGuard
// connector: the server's public key and the latest handshake.
func runAccessVPNReport() error {
	var report store.AccessVPNStatus
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return fmt.Errorf("read the VPN report: %w", err)
	}
	if len(report.PublicKey) > 64 || len(report.Error) > 512 {
		return fmt.Errorf("the VPN report exceeds its bounds")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := accessStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.ReportAccessVPN(ctx, report)
}

// runAccessVPNServerReport stores the server's public key and each peer's
// latest handshake, as observed by the host timer.
func runAccessVPNServerReport() error {
	var report struct {
		ServerPublicKey string `json:"server_public_key"`
		Error           string `json:"error"`
		Peers           []struct {
			PublicKey       string     `json:"public_key"`
			LatestHandshake *time.Time `json:"latest_handshake"`
		} `json:"peers"`
	}
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 256<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return fmt.Errorf("read the VPN server report: %w", err)
	}
	if len(report.ServerPublicKey) > 64 || len(report.Error) > 512 || len(report.Peers) > 1024 {
		return fmt.Errorf("the VPN server report exceeds its bounds")
	}
	handshakes := map[string]time.Time{}
	for _, p := range report.Peers {
		if p.LatestHandshake != nil {
			handshakes[p.PublicKey] = *p.LatestHandshake
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := accessStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.ReportAccessVPNServer(ctx, report.ServerPublicKey, report.Error, handshakes)
}
