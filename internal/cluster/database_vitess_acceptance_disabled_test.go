//go:build !hakopod_native_acceptance || !linux

package cluster

import "testing"

func TestVitessShippingBuildKeepsNativeAcceptanceClosed(t *testing.T) {
	if vitessNativeAcceptance {
		t.Fatal("shipping build enabled native Vitess acceptance")
	}
	if !vitessReleaseQualified && vitessRuntimeSupported(vitessTestDatabase().Spec) == nil {
		t.Fatal("shipping build admitted the unqualified Vitess runtime")
	}
}
