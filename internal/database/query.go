package database

import (
	"encoding/json"
	"fmt"
	"strings"
)

const QueryMaxRows = 1000
const QueryMaxBytes = 1 << 20
const QueryMaxSQLBytes = 64 << 10

type QueryRequest struct {
	SQL              string `json:"sql"`
	Parameters       []any  `json:"parameters"`
	ReadOnly         *bool  `json:"read_only,omitempty"`
	MaxRows          int    `json:"max_rows,omitempty"`
	MaxBytes         int    `json:"max_bytes,omitempty"`
	ExpectedRevision int64  `json:"expected_revision,omitempty"`
}

func (q QueryRequest) IsReadOnly() bool { return q.ReadOnly == nil || *q.ReadOnly }
func (q *QueryRequest) Validate() error {
	if q.ExpectedRevision < 0 {
		return fmt.Errorf("expected_revision must be positive when supplied")
	}
	if strings.TrimSpace(q.SQL) == "" || len(q.SQL) > QueryMaxSQLBytes || strings.ContainsRune(q.SQL, 0) {
		return fmt.Errorf("SQL must contain 1–65536 bytes without null characters")
	}
	if len(q.Parameters) > 100 {
		return fmt.Errorf("SQL accepts at most 100 parameters")
	}
	for _, p := range q.Parameters {
		switch p.(type) {
		case nil, string, bool, float64, json.Number:
		default:
			return fmt.Errorf("SQL parameters must be JSON scalar values")
		}
	}
	if q.MaxRows == 0 {
		q.MaxRows = 100
	}
	if q.MaxBytes == 0 {
		q.MaxBytes = 256 << 10
	}
	if q.MaxRows < 1 || q.MaxRows > QueryMaxRows || q.MaxBytes < 1024 || q.MaxBytes > QueryMaxBytes {
		return fmt.Errorf("query limits must be 1–1000 rows and 1024–1048576 bytes")
	}
	return nil
}

type QueryColumn struct {
	Name    string `json:"name"`
	TypeOID uint32 `json:"type_oid"`
}

// Text values preserve PostgreSQL numeric precision. SQL null remains JSON null.
type QueryResult struct {
	OperationID  string        `json:"operation_id"`
	DatabaseID   string        `json:"database_id"`
	ReadOnly     bool          `json:"read_only"`
	Columns      []QueryColumn `json:"columns"`
	Rows         [][]*string   `json:"rows"`
	RowsAffected int64         `json:"rows_affected"`
	Truncated    bool          `json:"truncated"`
	Outcome      string        `json:"outcome"`
}
type QueryError struct {
	Code    string
	Outcome string
}

func (e *QueryError) Error() string { return e.Code }
