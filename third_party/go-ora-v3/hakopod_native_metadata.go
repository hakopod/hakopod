//go:build hakopod_native_acceptance

package go_ora

import (
	"context"
	"database/sql/driver"
	"errors"
	"github.com/sijms/go-ora/v3/network"
	oraTypes "github.com/sijms/go-ora/v3/types"
	"io"
)

type NativeDiagnosticEvent struct {
	Stage         string `json:"stage"`
	Type          string `json:"type"`
	Size          string `json:"size"`
	FromServer    bool   `json:"from_server"`
	DescribedZero bool   `json:"described_zero"`
	Ordinal       string `json:"ordinal,omitempty"`
	Boundary      string `json:"boundary,omitempty"`
}
type nativeDiagnosticRecorder struct {
	events  []NativeDiagnosticEvent
	dropped bool
}

func (r *nativeDiagnosticRecorder) add(stage string, column *ParameterInfo) {
	if len(r.events) >= 16 {
		r.dropped = true
		return
	}
	switch stage {
	case "query_complete", "query_failed", "column", "rows_empty", "rows_present", "rows_complete", "rows_failed", "metadata_column_start", "metadata_column_end", "metadata_trailer_complete", "row_column_start", "row_column_end", "unknown_response":
	default:
		stage = "other"
	}
	event := NativeDiagnosticEvent{Stage: stage, Type: "none", Size: "none"}
	if column != nil {
		switch column.DataType {
		case 0:
			event.Type = "untyped"
		case oraTypes.NUMBER:
			event.Type = "number"
		case oraTypes.CHAR, oraTypes.NCHAR:
			event.Type = "character"
		case oraTypes.TIMESTAMP, oraTypes.TimeStampDTY:
			event.Type = "timestamp"
		case oraTypes.TIMESTAMPTZ:
			event.Type = "timestamp_tz"
		case oraTypes.TimeStampTZ_DTY:
			event.Type = "timestamp_tz_dty"
		case oraTypes.TimeStampLTZ, oraTypes.TimeStampLTZ_DTY:
			event.Type = "timestamp_ltz"
		case oraTypes.RAW:
			event.Type = "raw"
		default:
			event.Type = "other"
		}
		switch {
		case column.MaxLen == 0:
			event.Size = "zero"
		case column.MaxLen <= 32:
			event.Size = "small"
		case column.MaxLen <= 4096:
			event.Size = "medium"
		default:
			event.Size = "large"
		}
		event.FromServer = column.getDataFromServer
		event.DescribedZero = column.describedZero
	}
	r.events = append(r.events, event)
}

// NativeQueryMetadata is available only in explicitly tagged qualification builds.
// It returns static metadata. It never returns SQL, values, names, or backend errors.
func NativeQueryMetadata(ctx context.Context, conn *Connection, statement string, args []driver.NamedValue) []NativeDiagnosticEvent {
	recorder := &nativeDiagnosticRecorder{}
	stmt := NewStmt(statement, conn)
	stmt.nativeDiagnosticState.recorder = recorder
	defer stmt.Close()
	rows, err := stmt.QueryContext(ctx, args)
	if rows != nil {
		defer rows.Close()
	}
	if err != nil {
		recorder.add("query_failed", nil)
	} else {
		recorder.add("query_complete", nil)
	}
	for i := 0; i < len(stmt.columns) && len(recorder.events) < 16; i++ {
		recorder.add("column", &stmt.columns[i])
	}
	if dataset, ok := rows.(*DataSet); ok && dataset != nil && dataset.currentResultSet() != nil {
		if len(dataset.currentResultSet().rows) == 0 {
			recorder.add("rows_empty", nil)
		} else {
			recorder.add("rows_present", nil)
		}
	}
	return recorder.events
}

// NativeQueryMetadataCause reports only known static failure categories.
func NativeQueryMetadataCause(ctx context.Context, conn *Connection, statement string, args []driver.NamedValue) ([]NativeDiagnosticEvent, string) {
	recorder := &nativeDiagnosticRecorder{}
	stmt := NewStmt(statement, conn)
	stmt.nativeDiagnosticState.recorder = recorder
	defer stmt.Close()
	rows, err := stmt.QueryContext(ctx, args)
	if rows != nil {
		defer rows.Close()
	}
	cause := "none"
	if err != nil {
		cause = "other"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			cause = "deadline"
		case errors.Is(err, context.Canceled):
			cause = "cancelled"
		case errors.Is(err, network.ErrReadLimit):
			cause = "receive_limit"
		}
		if err.Error() == "TTC error: received code 3 during response reading" {
			cause = "ttc_response_code_3"
		}
		recorder.add("query_failed", nil)
	} else {
		recorder.add("query_complete", nil)
	}
	for i := 0; i < len(stmt.columns) && len(recorder.events) < 16; i++ {
		recorder.add("column", &stmt.columns[i])
	}
	if dataset, ok := rows.(*DataSet); ok && dataset != nil && dataset.currentResultSet() != nil {
		if len(dataset.currentResultSet().rows) == 0 {
			recorder.add("rows_empty", nil)
		} else {
			recorder.add("rows_present", nil)
		}
	}
	if err == nil && rows != nil {
		if len(stmt.columns) < 1 || len(stmt.columns) > 2 {
			recorder.add("rows_failed", nil)
			return recorder.events, "row_limit"
		}
		done := conn.session.StartContext(ctx)
		defer conn.session.EndContext(done)
		values := make([]driver.Value, 2)
		count := 0
		for calls := 0; calls < 2; calls++ {
			nextErr := rows.Next(values)
			clear(values)
			if nextErr == io.EOF {
				if count != 1 {
					cause = "row_limit"
					recorder.add("rows_failed", nil)
					break
				}
				recorder.add("rows_complete", nil)
				break
			}
			if nextErr != nil {
				cause = "other"
				if errors.Is(nextErr, context.DeadlineExceeded) {
					cause = "deadline"
				}
				if errors.Is(nextErr, context.Canceled) {
					cause = "cancelled"
				}
				if errors.Is(nextErr, network.ErrReadLimit) {
					cause = "receive_limit"
				}
				if nextErr.Error() == "TTC error: received code 3 during response reading" {
					cause = "ttc_response_code_3"
				}
				recorder.add("rows_failed", nil)
				break
			}
			count++
			if count > 1 {
				cause = "row_limit"
				recorder.add("rows_failed", nil)
				break
			}
		}
	}
	return recorder.events, cause
}

// nativeDiagnosticState belongs only to the fresh statement explicitly captured
// by NativeQueryMetadata or NativeQueryMetadataCause. Connections are unchanged.
type nativeDiagnosticState struct {
	recorder        *nativeDiagnosticRecorder
	boundary        string
	trailerComplete bool
}

func (stmt *defaultStmt) nativeBoundary(stage string, ordinal int, column *ParameterInfo) {
	state := &stmt.nativeDiagnosticState
	if state.recorder == nil {
		return
	}
	switch stage {
	case "metadata_column_start", "metadata_column_end", "metadata_trailer_complete", "row_column_start", "row_column_end", "unknown_response":
	default:
		stage = "other"
	}
	prior := state.boundary
	state.boundary = stage
	before := len(state.recorder.events)
	state.recorder.add(stage, column)
	if len(state.recorder.events) == before {
		return
	}
	event := &state.recorder.events[before]
	switch {
	case ordinal < 0:
		event.Ordinal = "none"
	case ordinal == 0:
		event.Ordinal = "first"
	case ordinal == 1:
		event.Ordinal = "second"
	default:
		event.Ordinal = "later"
	}
	if stage == "unknown_response" {
		switch prior {
		case "metadata_column_start", "metadata_column_end", "metadata_trailer_complete", "row_column_start", "row_column_end":
			event.Boundary = prior
		default:
			event.Boundary = "other"
		}
	}
}

func (stmt *defaultStmt) nativeTrailerStart() {
	if stmt.recorder != nil {
		stmt.trailerComplete = true
	}
}
func (stmt *defaultStmt) nativeTrailerRead(ok bool) {
	if stmt.recorder != nil {
		stmt.trailerComplete = stmt.trailerComplete && ok
	}
}
func (stmt *defaultStmt) nativeTrailerEnd() {
	if stmt.recorder != nil && stmt.trailerComplete {
		stmt.nativeBoundary("metadata_trailer_complete", -1, nil)
	}
}

func (stmt *defaultStmt) nativeResponseFailure(err error) {
	if stmt.recorder != nil && err != nil && err.Error() == "TTC error: received code 3 during response reading" {
		stmt.nativeBoundary("unknown_response", -1, nil)
	}
}
