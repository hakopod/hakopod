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
	ExecutionMode    string `json:"execution_mode,omitempty"`
}

func (q QueryRequest) IsReadOnly() bool { return q.ReadOnly == nil || *q.ReadOnly }
func (q *QueryRequest) Validate() error {
	if q.ExecutionMode != "" && q.ExecutionMode != "transaction" && q.ExecutionMode != "nontransactional" {
		return fmt.Errorf("execution_mode must be transaction or nontransactional")
	}
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
	Name     string `json:"name"`
	TypeOID  uint32 `json:"type_oid"`
	TypeName string `json:"type_name,omitempty"`
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

// QueryCapabilities describes engine guarantees. It does not grant permission.
type QueryCapabilities struct {
	Engine              string   `json:"engine"`
	ReadOnlySupported   bool     `json:"read_only_supported"`
	Supported           bool     `json:"supported"`
	ExecutionModes      []string `json:"execution_modes"`
	ApplicationIdentity string   `json:"application_identity"`
	ParameterStyle      string   `json:"parameter_style"`
	ReadOnlyEnforcement string   `json:"read_only_enforcement"`
	TransactionalDML    bool     `json:"transactional_dml"`
	TransactionalDDL    bool     `json:"transactional_ddl"`
	DDLCommit           string   `json:"ddl_commit"`
	Cancellation        string   `json:"cancellation"`
}

func SQLQueryEngine(engine string) bool {
	switch engine {
	case "postgresql", "mysql", "vitess", "duckdb", "clickhouse", "oracle":
		return true
	}
	return false
}
func CapabilitiesForQuery(engine string) QueryCapabilities {
	c := QueryCapabilities{ExecutionModes: []string{}, ApplicationIdentity: "app", Engine: engine, Cancellation: "The connection closes on cancellation. Check unknown write outcomes before retrying."}
	switch engine {
	case "postgresql":
		c.Supported = true
		c.ReadOnlySupported = true
		c.ExecutionModes = []string{"transaction"}
		c.ParameterStyle = "$1"
		c.ReadOnlyEnforcement = "server_transaction"
		c.TransactionalDML = true
		c.TransactionalDDL = true
		c.DDLCommit = "transaction"
	case "mysql", "vitess":
		if engine == "mysql" {
			c.Supported = true
			c.ReadOnlySupported = true
			c.ExecutionModes = []string{"transaction", "nontransactional"}
		}
		c.ParameterStyle = "?"
		c.ReadOnlyEnforcement = "server_transaction"
		c.TransactionalDML = true
		c.DDLCommit = "implicit_commit"
	case "oracle":
		c.ApplicationIdentity = "APP"
		c.ParameterStyle = ":1"
		c.ReadOnlyEnforcement = "server_transaction"
		c.TransactionalDML = true
		c.DDLCommit = "implicit_commit"
	case "duckdb":
		c.Supported = true
		c.ExecutionModes = []string{"nontransactional"}
		c.ApplicationIdentity = "postgres (managed application owner)"
		c.ParameterStyle = "$1"
		c.ReadOnlyEnforcement = "unsupported"
		c.DDLCommit = "nontransactional"
	case "clickhouse":
		c.Supported = true
		c.ReadOnlySupported = true
		c.ExecutionModes = []string{"nontransactional"}
		c.ParameterStyle = "{p1:Type}"
		c.ReadOnlyEnforcement = "server_readonly_setting"
		c.DDLCommit = "nontransactional"
	}
	return c
}
