//go:build hakopod_native_acceptance

package cluster

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/sijms/go-ora/v3/network"
	"io"
	"time"
)

type sqlDiagnosticKey struct{}
type sqlDiagnosticEvent struct {
	Stage, Elapsed, Cause, Outcome string
	ContextExpired                 bool
}
type sqlDiagnosticRecorder struct {
	started time.Time
	events  []sqlDiagnosticEvent
}

func sqlQueryDiagnosticStage(ctx context.Context, stage string, err error) {
	recorder, ok := ctx.Value(sqlDiagnosticKey{}).(*sqlDiagnosticRecorder)
	if !ok || len(recorder.events) >= 24 {
		return
	}
	switch stage {
	case "adapter_start", "observe", "credentials", "connector", "connection", "decoder_limit", "authority", "prepare", "query", "columns", "scan", "rows", "terminal":
	default:
		stage = "other"
	}
	elapsed := "under_1s"
	switch d := time.Since(recorder.started); {
	case d >= 20*time.Second:
		elapsed = "at_least_20s"
	case d >= 15*time.Second:
		elapsed = "15_to_20s"
	case d >= 5*time.Second:
		elapsed = "5_to_15s"
	case d >= time.Second:
		elapsed = "1_to_5s"
	}
	cause := sqlDiagnosticCause(err)
	outcome := "none"
	var failure *database.QueryError
	if errors.As(err, &failure) {
		switch failure.Outcome {
		case "not_started", "unknown", "rolled_back":
			outcome = failure.Outcome
		default:
			outcome = "other"
		}
	}
	recorder.events = append(recorder.events, sqlDiagnosticEvent{Stage: stage, Elapsed: elapsed, Cause: cause, Outcome: outcome, ContextExpired: ctx.Err() != nil})
}

type sqlDiagnosticStaticError string

func (e sqlDiagnosticStaticError) Error() string { return "static diagnostic failure" }

func sqlDiagnosticCause(err error) string {
	var static sqlDiagnosticStaticError
	if errors.As(err, &static) {
		switch string(static) {
		case "deadline", "cancelled", "receive_limit", "ttc_response_code_3", "row_limit", "other":
			return string(static)
		default:
			return "other"
		}
	}
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, network.ErrReadLimit):
		return "receive_limit"
	case errors.Is(err, driver.ErrBadConn):
		return "bad_connection"
	case errors.Is(err, sql.ErrConnDone):
		return "connection_done"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected_eof"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, network.ErrConnReset):
		return "driver_context_reset"
	}
	switch err.Error() {
	case "TTC error: received code 3 during response reading":
		return "ttc_response_code_3"
	case "attempt to set timeout on closed connection", "attempt to write on closed connection", "closed connection":
		return "closed_connection"
	case "incorrect format for DBTimeZone":
		return "timezone_decode"
	}
	return "other"
}
