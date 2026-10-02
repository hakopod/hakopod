//go:build !hakopod_native_acceptance || !linux

package main

import (
	"strings"
	"testing"
)

func TestShippingServerRejectsAcceptanceBeforeDatabaseAccess(t *testing.T) {
	t.Setenv("HAKOPOD_CONFIG_FILE", "")
	t.Setenv("HAKOPOD_DEPLOYMENT_MODE", "self-hosted")
	t.Setenv("HAKOPOD_NATIVE_ACCEPTANCE_CONFIG_FILE", "/protected/native-run.json")
	t.Setenv("HAKOPOD_DATABASE_URL", "")
	t.Setenv("HAKOPOD_DATABASE_URL_FILE", "/unavailable/control-database.dsn")
	if err := run(); err == nil || !strings.Contains(err.Error(), "separately compiled development binary") {
		t.Fatalf("shipping server did not reject acceptance before reading database configuration: %v", err)
	}
}
