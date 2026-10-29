package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

// runAccessReset is the break-glass path for a management access policy that
// locks every administrator out. It runs on the server with the API's own
// settings, so it needs no network path to the dashboard and no license.
// It ships with every release binary, unlike the maintenance helper.
func runAccessReset() error {
	if err := loadOperatorConfig(); err != nil {
		return err
	}
	dbURL, err := secretSetting("HAKOPOD_DATABASE_URL")
	if err != nil || dbURL == "" {
		return fmt.Errorf("HAKOPOD_DATABASE_URL or HAKOPOD_DATABASE_URL_FILE is required; run with the API environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Open(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect to PostgreSQL: check HAKOPOD_DATABASE_URL and database health")
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
