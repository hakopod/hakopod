package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var oracleDiagnosticCode = regexp.MustCompile(`\b(?:ORA|PLS|SP2)-[0-9]{4,5}\b`)
var oracleDiagnosticObject = regexp.MustCompile(`\bHAKOPOD_OBJECT=([A-Z_]+)\b`)

const oracleDiagnosticObjectTypes = "USER,SYSTEM_GRANT,ROLE_GRANT,DEFAULT_ROLE,TABLESPACE_QUOTA,PRE_SCHEMA,PROCACT_SCHEMA,POST_SCHEMA,TABLE,TABLE_DATA,INDEX,CONSTRAINT,REF_CONSTRAINT,TRIGGER,PROCEDURE,FUNCTION,PACKAGE,PACKAGE_BODY,TYPE,TYPE_BODY,VIEW,SEQUENCE,SYNONYM,MATERIALIZED_VIEW,STATISTICS,OBJECT_GRANT,GRANT"

// Oracle output can include SQL and credentials. Surface only bounded error
// identifiers and known metadata categories, never names, SQL or raw messages.
func oracleErrorSuffix(output string) string {
	codes := []string{}
	seen := map[string]bool{}
	for _, code := range oracleDiagnosticCode.FindAllString(output, -1) {
		if !seen[code] {
			codes = append(codes, code)
			seen[code] = true
		}
		if len(codes) == 8 {
			break
		}
	}
	for _, match := range oracleDiagnosticObject.FindAllStringSubmatch(output, -1) {
		kind := match[1]
		if strings.Contains(","+oracleDiagnosticObjectTypes+",", ","+kind+",") && !seen[kind] && len(codes) < 8 {
			codes = append(codes, "object type "+kind)
			seen[kind] = true
		}
	}
	if len(codes) == 0 {
		return ""
	}
	return " (" + strings.Join(codes, ", ") + ")"
}

func (c *Client) oracleLocalQuery(ctx context.Context, d database.Resource, m database.Member, query string) (string, error) {
	output := &databaseBoundedWriter{limit: 32 << 10}
	input := oracleSQLInput(query)
	if err := c.DatabaseExec(ctx, d, m, []string{"sqlplus", "-s", "/", "as", "sysdba"}, strings.NewReader(input), output); err != nil {
		return "", fmt.Errorf("Oracle query did not complete%s", oracleErrorSuffix(output.String()))
	}
	return strings.TrimSpace(output.String()), nil
}

func oracleSQLInput(query string) string {
	query = strings.TrimSpace(query)
	if !strings.HasSuffix(query, "/") && !strings.HasSuffix(query, ";") {
		query += ";"
	}
	return "whenever sqlerror exit failure\nwhenever oserror exit failure\nset heading off feedback off echo off verify off pagesize 0 linesize 32767 long 32767 trimspool on\n" + query + "\nexit\n"
}

func (c *Client) observeOracleDatabase(ctx context.Context, d database.Resource, object *unstructured.Unstructured, o *database.Observation) error {
	if err := oracleRuntimeSupported(d.Spec); err != nil {
		return err
	}
	if len(o.Members) != 1 {
		return fmt.Errorf("Oracle has no unique instance")
	}
	if err := c.oracleFreeRevisionReady(ctx, d, object); err != nil {
		return err
	}
	query := `SELECT JSON_OBJECT('role' VALUE database_role, 'mode' VALUE open_mode,
 'version' VALUE (SELECT version_full FROM v$instance),
 'pdb' VALUE (SELECT open_mode FROM v$pdbs WHERE name='FREEPDB1')) FROM v$database`
	output, err := c.oracleLocalQuery(ctx, d, o.Members[0], query)
	if err != nil {
		return err
	}
	var health struct {
		Role    string `json:"role"`
		Mode    string `json:"mode"`
		Version string `json:"version"`
		PDB     string `json:"pdb"`
	}
	if json.Unmarshal([]byte(output), &health) != nil || health.Role != "PRIMARY" || health.Mode != "READ WRITE" || health.PDB != "READ WRITE" || !strings.HasPrefix(health.Version, "23.26.") {
		return fmt.Errorf("Oracle role, PDB state or version is not healthy")
	}
	step, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	client, err := c.oracleApplicationConnection(step, d, o.Members[0], true)
	if err != nil {
		return err
	}
	defer client.Close()
	var identity string
	if err = client.QueryRowContext(step, "SELECT SYS_CONTEXT('USERENV','SESSION_USER')||'|'||SYS_CONTEXT('USERENV','CON_NAME') FROM dual").Scan(&identity); err != nil || identity != "APP|FREEPDB1" {
		return fmt.Errorf("Oracle application authentication or TCPS verification failed")
	}
	o.Primary = o.Members[0].Name
	o.Members[0].Role = "primary"
	o.TopologyFingerprint = fmt.Sprintf("%x", sha256.Sum256([]byte(o.Primary+":"+o.Members[0].UID)))
	o.Endpoints = []database.Endpoint{{Purpose: "read_write", Host: oracleHost(d), Port: 2484}}
	return nil
}

func (c *Client) oracleEngineMetrics(ctx context.Context, d database.Resource, o database.Observation) (database.EngineMetrics, error) {
	var metric database.EngineMetrics
	m, err := oracleObservedPrimary(d, o)
	if err != nil {
		return metric, err
	}
	// These dynamic views do not use AWR, ASH, ADDM, or licensed packs.
	query := `SELECT JSON_OBJECT(
 'connections' VALUE (SELECT COUNT(*) FROM v$session WHERE username='APP'),
 'active_connections' VALUE (SELECT COUNT(*) FROM v$session WHERE username='APP' AND status='ACTIVE'),
 'max_connections' VALUE (SELECT TO_NUMBER(value) FROM v$parameter WHERE name='sessions'),
 'data_bytes' VALUE (SELECT NVL(SUM(bytes),0) FROM cdb_segments WHERE owner='APP'),
 'transactions' VALUE (SELECT SUM(value) FROM v$sysstat WHERE name IN ('user commits','user rollbacks')),
 'uptime_seconds' VALUE (SELECT FLOOR((SYSDATE-startup_time)*86400) FROM v$instance)) FROM dual`
	output, err := c.oracleLocalQuery(ctx, d, m, query)
	if err != nil {
		return metric, err
	}
	if json.Unmarshal([]byte(output), &metric) != nil || !validDatabaseEngineMetrics(metric) {
		return metric, fmt.Errorf("Oracle statistics are incomplete")
	}
	now := time.Now().UTC()
	metric.Available = true
	metric.SampledAt = &now
	return metric, nil
}
