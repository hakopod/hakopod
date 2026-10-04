//go:build hakopod_native_acceptance && linux

package cluster

import (
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func TestVitessNativeAcceptanceBuildAdmitsOnlyValidSpec(t *testing.T) {
	if !vitessNativeAcceptance {
		t.Fatal("tagged Linux build did not enable native Vitess acceptance")
	}
	valid := vitessTestDatabase().Spec
	if err := vitessRuntimeSupported(valid); err != nil {
		t.Fatal("tagged Linux build rejected valid Vitess acceptance spec", err)
	}
	invalid := database.Spec{SchemaVersion: 1, Engine: "vitess"}
	if err := vitessRuntimeSupported(invalid); err == nil {
		t.Fatal("tagged Linux build bypassed Vitess specification validation")
	}
}
