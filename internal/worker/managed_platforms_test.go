package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/store"
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
		{"safe stage", &cluster.ManagedPlatformRuntimeError{Category: "supabase_database_tls_validation", Err: errors.New("secret material")}, "supabase_database_tls_validation"},
		{"safe apply stage", &cluster.ManagedPlatformRuntimeError{Category: "supabase_apply_statefulset", Err: errors.New("password=must-not-escape")}, "supabase_apply_statefulset"},
		{"safe credential rotation stage", &cluster.ManagedPlatformRuntimeError{Category: "supabase_rotate_database_credentials", Err: errors.New("password=must-not-escape")}, "supabase_rotate_database_credentials"},
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
