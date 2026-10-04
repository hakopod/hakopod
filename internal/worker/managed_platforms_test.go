package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestManagedPlatformErrorObservation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "context_deadline"},
		{"cancelled", context.Canceled, "context_cancelled"},
		{"store conflict", fmt.Errorf("wrapped: %w", store.ErrConflict), "store_conflict"},
		{"stage preserves store conflict", &cluster.ManagedPlatformRuntimeError{Category: "capacity_admission", Err: store.ErrConflict}, "store_conflict"},
		{"kubernetes conflict", apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, "redacted", errors.New("redacted")), "kubernetes_conflict"},
		{"PostgreSQL serialization", &pgconn.PgError{Code: "40001", Message: "secret material"}, "postgres_serialization"},
		{"safe stage", &cluster.ManagedPlatformRuntimeError{Category: "supabase_database_tls_validation", Err: errors.New("secret material")}, "supabase_database_tls_validation"},
		{"safe apply stage", &cluster.ManagedPlatformRuntimeError{Category: "supabase_apply_statefulset", Err: errors.New("password=must-not-escape")}, "supabase_apply_statefulset"},
		{"safe credential rotation stage", &cluster.ManagedPlatformRuntimeError{Category: "supabase_rotate_database_credentials", Err: errors.New("password=must-not-escape")}, "supabase_rotate_database_credentials"},
		{"safe Neon provider absence stage", &cluster.ManagedPlatformRuntimeError{Category: "neon_provider_state", Err: errors.New("password=must-not-escape")}, "neon_provider_state"},
		{"safe Neon TLS preparation stage", &cluster.ManagedPlatformRuntimeError{Category: "neon_tls_prepare", Err: errors.New("password=must-not-escape")}, "neon_tls_prepare"},
		{"safe Neon lifecycle preparation stage", &cluster.ManagedPlatformRuntimeError{Category: "neon_lifecycle_prepare", Err: errors.New("password=must-not-escape")}, "neon_lifecycle_prepare"},
		{"safe Neon lifecycle deletion stage", &cluster.ManagedPlatformRuntimeError{Category: "neon_lifecycle_deprovision", Err: errors.New("password=must-not-escape")}, "neon_lifecycle_deprovision"},
		{"unapproved stage", &cluster.ManagedPlatformRuntimeError{Category: "attacker supplied", Err: errors.New("secret material")}, "other"},
		{"other", errors.New("secret material"), "other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			category, errorType := managedPlatformErrorObservation(test.err)
			if category != test.want {
				t.Fatalf("category = %q, want %q", category, test.want)
			}
			if category == "secret material" || errorType == "secret material" {
				t.Fatal("raw error text escaped into the observation")
			}
		})
	}
}

func TestManagedPlatformNeonSafeStages(t *testing.T) {
	for _, stage := range []string{
		"neon_proxy_activate", "neon_namespace", "neon_claims", "neon_provider_state", "neon_runtime_repair",
		"neon_tls_prepare", "neon_lifecycle_prepare", "neon_lifecycle_deprovision", "neon_node_inventory",
		"neon_recovery_binding", "neon_controller_secret", "neon_render", "neon_secret_validate", "neon_secret_apply",
		"neon_object_apply", "neon_bootstrap_observe", "neon_lifecycle_provision", "neon_compute_replay",
		"neon_serving_observe", "neon_snapshot_prune", "neon_recovery_observe",
	} {
		t.Run(stage, func(t *testing.T) {
			category, kind := managedPlatformErrorObservation(&cluster.ManagedPlatformRuntimeError{Category: stage, Err: errors.New("password=must-not-escape")})
			if category != stage || kind != "*cluster.ManagedPlatformRuntimeError" {
				t.Fatalf("safe Neon stage lost its bounded error observation: category=%q type=%q", category, kind)
			}
		})
	}
}
