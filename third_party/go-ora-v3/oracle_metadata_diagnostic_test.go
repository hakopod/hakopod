//go:build hakopod_native_acceptance

package go_ora

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOracleMetadataRecorderPrivacyAndBound(t *testing.T) {
	r := &nativeDiagnosticRecorder{}
	col := &ParameterInfo{Name: "private-column", Value: "private-value"}
	col.TypeName = "private-type"
	for i := 0; i < 100; i++ {
		r.add("private-stage", col)
	}
	data, err := json.Marshal(r.events)
	if err != nil || len(r.events) != 16 || !r.dropped {
		t.Fatal("metadata recorder is not bounded")
	}
	if strings.Contains(string(data), "private") || len(data) > 2048 {
		t.Fatal("metadata recorder disclosed arbitrary input")
	}
}
