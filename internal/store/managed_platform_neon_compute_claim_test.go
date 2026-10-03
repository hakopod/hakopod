package store

import (
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func TestNeonComputeConfigClaimAdmission(t *testing.T) {
	op := ManagedPlatformOperation{
		Revision: 7,
		Spec:     managedplatform.Spec{Kind: "neon", Neon: &managedplatform.NeonConfig{ComputeReplicas: 2}},
		Plan:     managedplatform.Plan{Components: []managedplatform.Component{{Name: "compute"}, {Name: "compute-tls"}}},
	}
	for _, name := range []string{
		"neon-compute-0-tls-r7", "neon-compute-1-tls-r7",
		"neon-compute-0-tls-r7-0123456789abcdef", "neon-compute-1-tls-r7-ffffffffffffffff",
	} {
		if !managedPlatformClaimComponentAllowed(op, "configmap."+name, "runtime_component") {
			t.Fatalf("current compute config snapshot was refused: %s", name)
		}
	}
	for _, name := range []string{
		"neon-compute-2-tls-r7-0123456789abcdef", "neon-compute-01-tls-r7-0123456789abcdef",
		"neon-compute-0-tls-r6-0123456789abcdef", "neon-compute-0-tls-r8-0123456789abcdef",
		"neon-compute-0-tls-r07-0123456789abcdef", "neon-compute-0-tls-r7-",
		"neon-compute-0-tls-r7-0123456789abcde", "neon-compute-0-tls-r7-0123456789abcdef0",
		"neon-compute-0-tls-r7-0123456789abcdeF", "neon-compute-0-tls-r7-0123456789abcdeg",
		"neon-compute-0-tls-r7-0123456789abcde-", "neon-compute-0-tls-r7-0123456789abcdef-extra",
	} {
		if managedPlatformClaimComponentAllowed(op, "configmap."+name, "runtime_component") {
			t.Fatalf("unowned compute config name crossed admission: %s", name)
		}
	}
	component := "configmap.neon-compute-0-tls-r7-0123456789abcdef"
	for _, kind := range []string{"neon_tenant", "neon_timeline"} {
		if managedPlatformClaimComponentAllowed(op, component, kind) {
			t.Fatal("compute config acquired provider resource authority")
		}
	}
	if managedPlatformClaimComponentAllowed(op, strings.Replace(component, "configmap.", "secret.", 1), "runtime_component") {
		t.Fatal("compute config acquired secret authority")
	}
	for _, member := range []string{"compute", "compute-tls"} {
		op.Plan.Components = []managedplatform.Component{{Name: member}}
		if managedPlatformClaimComponentAllowed(op, component, "runtime_component") {
			t.Fatal("partial compute plan acquired config authority")
		}
	}
}
