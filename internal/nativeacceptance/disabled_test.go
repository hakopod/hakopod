//go:build !hakopod_native_acceptance || !linux

package nativeacceptance

import (
	"context"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func TestShippingBinaryRejectsNativeConfiguration(t *testing.T) {
	t.Setenv("HAKOPOD_NATIVE_ACCEPTANCE_CONFIG_FILE", "/protected/native.json")
	if _, err := Configure(context.Background()); err == nil {
		t.Fatal("shipping binary accepted native qualification")
	}
	plan := managedplatform.Plan{Capability: managedplatform.Capability{Reason: "unqualified"}}
	if got := Plan("native-run", "development", "neon", plan); got.Capability != plan.Capability {
		t.Fatal("shipping capability changed")
	}
	if AllowsUnboundOperatorQualification(context.Background(), "neon") {
		t.Fatal("shipping binary allowed an unbound operator qualification")
	}
}
