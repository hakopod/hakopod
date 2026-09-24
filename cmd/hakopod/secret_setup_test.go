package main

import (
	"context"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
)

func TestSecretSetupNoninteractiveDoesNotSubmit(t *testing.T) {
	// A nil client proves no credential write or deploy call occurs in this branch.
	err := setupDeploymentSecrets(context.Background(), nil, "project", "main", spec.Application{Name: "app"}, []string{"database-password"}, true)
	if err == nil || !strings.Contains(err.Error(), "database-password") || !strings.Contains(err.Error(), "No deployment was submitted") {
		t.Fatal(err)
	}
	if err = setupDeploymentSecrets(context.Background(), nil, "project", "main", spec.Application{Name: "app"}, nil, true); err != nil {
		t.Fatal(err)
	}
}
