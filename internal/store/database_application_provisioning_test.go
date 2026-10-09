package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
)

func TestApplicationProvisioningRejectsInvalidBindingWithoutReview(t *testing.T) {
	s, p, d := databaseFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptDatabase(ctx, p, d, 0, "create-provisioning-validation", "create"); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, op, database.Observation{Status: "ready", Revision: 1, ObservedAt: time.Now().UTC()}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	app, err := spec.Normalize(spec.Application{SchemaVersion: 1, Name: "provisioning-validation", Services: map[string]spec.Service{"main": {Image: "example.invalid/app@sha256:" + strings.Repeat("a", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.Accept(ctx, p, d.Project, d.Environment, app, 0, "create-provisioning-app")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct{ variable, endpoint string }{{"not valid", "read_write"}, {"DATABASE_URL", "arbitrary"}} {
		_, err = s.PlanDatabaseApplicationProvisioning(ctx, p, d.ID, release.ApplicationID, "main", input.variable, input.endpoint, "hp_valid", "hp_valid", "database-password", []byte("encrypted-password-material-12345"))
		if !errors.Is(err, ErrInput) {
			t.Fatalf("invalid binding accepted: %q %q: %v", input.variable, input.endpoint, err)
		}
	}
	var reviews int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM managed_database_reviews WHERE kind='application-provisioning'`).Scan(&reviews); err != nil || reviews != 0 {
		t.Fatalf("invalid plan left a review: %d %v", reviews, err)
	}
}
