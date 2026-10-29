package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	state := struct {
		Enabled    bool     `json:"enabled"`
		AllowCIDRs []string `json:"allow_cidrs"`
	}{policy.Enabled && policy.Firewall, []string{}}
	if state.Enabled {
		state.AllowCIDRs = policy.AllowCIDRs
	}
	return json.NewEncoder(os.Stdout).Encode(state)
}
