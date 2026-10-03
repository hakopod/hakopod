package store

import (
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

func TestNeonClaimAdmissionAllowsOnlyRevisionedProxyControlPlaneCA(t *testing.T) {
	op := ManagedPlatformOperation{
		ID:         strings.Repeat("b", 32),
		PlatformID: strings.Repeat("a", 32),
		Revision:   7,
		Kind:       "create",
		Spec: managedplatform.Spec{
			Kind: "neon",
			Neon: &managedplatform.NeonConfig{ComputeReplicas: 1, Pageservers: 2, Safekeepers: 3},
		},
		Plan: managedplatform.Plan{Components: []managedplatform.Component{{Name: "proxy"}}},
	}
	if !managedPlatformClaimComponentAllowed(op, "configmap.neon-proxy-control-plane-ca-r7", "runtime_component") {
		t.Fatal("revisioned proxy control-plane CA ConfigMap is outside claim admission")
	}
	for _, component := range []string{"configmap.neon-proxy-control-plane-ca-r6", "configmap.neon-proxy-control-plane-ca-r8", "configmap.neon-proxy-control-plane-ca"} {
		if managedPlatformClaimComponentAllowed(op, component, "runtime_component") {
			t.Fatalf("unexpected proxy CA ConfigMap crossed claim admission: %s", component)
		}
	}
}
