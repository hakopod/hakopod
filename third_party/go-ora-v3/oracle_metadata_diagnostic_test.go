//go:build hakopod_native_acceptance

package go_ora

import (
	"encoding/json"
	"errors"
	oraTypes "github.com/sijms/go-ora/v3/types"
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

func TestOracleBoundaryRecorderOptInPrivacyAndBound(t *testing.T) {
	stmt := &defaultStmt{text: "private-sql"}
	stmt.nativeBoundary("row_column_start", 0, nil)
	if stmt.recorder != nil {
		t.Fatal("ordinary statement opted into capture")
	}
	r := &nativeDiagnosticRecorder{}
	stmt.recorder = r
	col := &ParameterInfo{Name: "private-column", Value: "private-value"}
	col.TypeName = "private-type"
	stmt.nativeBoundary("metadata_column_end", 1, col)
	stmt.nativeBoundary("unknown_response", -1, nil)
	if len(r.events) != 2 || r.events[0].Ordinal != "second" || r.events[1].Boundary != "metadata_column_end" {
		t.Fatal("static boundary context missing")
	}
	for i := 0; i < 100; i++ {
		stmt.nativeBoundary("private-stage", i, col)
	}
	data, err := json.Marshal(r.events)
	if err != nil || len(r.events) != 16 || !r.dropped || strings.Contains(string(data), "private") || len(data) > 4096 {
		t.Fatal("boundary capture is unbounded or disclosed arbitrary input")
	}
}

func TestOracleBoundaryCaptureIsStatementScoped(t *testing.T) {
	conn := &Connection{}
	captured := NewStmt("private-sql", conn)
	other := NewStmt("private-sql", conn)
	captured.recorder = &nativeDiagnosticRecorder{}
	other.nativeBoundary("row_column_start", 0, nil)
	captured.nativeBoundary("row_column_start", 0, nil)
	if other.recorder != nil || len(captured.recorder.events) != 1 {
		t.Fatal("capture escaped the explicitly opted-in statement")
	}
}

func TestOracleBoundaryResponseAndTrailerAccuracy(t *testing.T) {
	stmt := &defaultStmt{}
	stmt.recorder = &nativeDiagnosticRecorder{}
	stmt.nativeResponseFailure(errors.New("private-error"))
	if len(stmt.recorder.events) != 0 {
		t.Fatal("ordinary response error labeled unknown")
	}
	stmt.nativeTrailerStart()
	stmt.nativeTrailerRead(false)
	stmt.nativeTrailerRead(true)
	stmt.nativeTrailerEnd()
	if len(stmt.recorder.events) != 0 {
		t.Fatal("failed trailer labeled complete")
	}
	stmt.nativeTrailerStart()
	stmt.nativeTrailerRead(true)
	stmt.nativeTrailerEnd()
	stmt.nativeResponseFailure(errors.New("TTC error: received code 3 during response reading"))
	if len(stmt.recorder.events) != 2 || stmt.recorder.events[1].Boundary != "metadata_trailer_complete" {
		t.Fatal("known static response boundary missing")
	}
}

func TestOracleMetadataTimestampCategories(t *testing.T) {
	for _, sample := range []struct {
		typ      uint16
		category string
	}{
		{oraTypes.TIMESTAMP, "timestamp"}, {oraTypes.TimeStampDTY, "timestamp"},
		{oraTypes.TIMESTAMPTZ, "timestamp_tz"}, {oraTypes.TimeStampTZ_DTY, "timestamp_tz_dty"},
		{oraTypes.TimeStampLTZ, "timestamp_ltz"}, {oraTypes.TimeStampLTZ_DTY, "timestamp_ltz"},
	} {
		r := &nativeDiagnosticRecorder{}
		col := &ParameterInfo{Name: "private-column", Value: "private-value"}
		col.DataType = sample.typ
		r.add("column", col)
		encoded, err := json.Marshal(r.events)
		if err != nil || len(r.events) != 1 || r.events[0].Type != sample.category || strings.Contains(string(encoded), "private") || len(encoded) > 256 {
			t.Fatal("timestamp diagnostic category is unsafe or ambiguous")
		}
	}
}

func TestOracleMetadataDescribedZeroPrivacy(t *testing.T) {
	for _, zero := range []bool{false, true} {
		r := &nativeDiagnosticRecorder{}
		col := &ParameterInfo{describedZero: zero, Name: "private-column", Value: "private-value"}
		r.add("column", col)
		encoded, err := json.Marshal(r.events)
		if err != nil || r.events[0].DescribedZero != zero || strings.Contains(string(encoded), "private") || len(encoded) > 256 {
			t.Fatal("original describe boolean was unsafe or missing")
		}
	}
}
