//go:build hakopod_native_acceptance

package go_ora

import (
	"context"
	"database/sql/driver"
	oraTypes "github.com/sijms/go-ora/v3/types"
)

type NativeDiagnosticEvent struct {
	Stage      string `json:"stage"`
	Type       string `json:"type"`
	Size       string `json:"size"`
	FromServer bool   `json:"from_server"`
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
	case "query_complete", "query_failed", "column", "rows_empty", "rows_present":
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
	}
	r.events = append(r.events, event)
}

// NativeQueryMetadata is available only in explicitly tagged qualification builds.
// It returns static metadata. It never returns SQL, values, names, or backend errors.
func NativeQueryMetadata(ctx context.Context, conn *Connection, statement string, args []driver.NamedValue) []NativeDiagnosticEvent {
	recorder := &nativeDiagnosticRecorder{}
	stmt := NewStmt(statement, conn)
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
