//go:build !hakopod_native_acceptance

package cluster

import "testing"

func TestSupabaseRotationAcceptanceBoundaryDisabled(t *testing.T) {
	t.Setenv("HAKOPOD_NATIVE_SUPABASE_ROTATION_FAIL_ONCE_FILE", "/should/not/be/created")
	if err := supabaseRotationAcceptanceBoundary(); err != nil {
		t.Fatal(err)
	}
}
