package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
	"k8s.io/client-go/tools/clientcmd"
)

// This test uses real HTTP, the canonical MCP adapter and an owned PostgreSQL
// database. Its control-plane identity and state are disposable development data.
func TestDatabaseQueryAPIMCPLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_QUERY_API_TEST") != "1" {
		t.Skip("enable owned development API and MCP SQL acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("SQL acceptance requires k3d-hakopod-dev")
	}
	runtime, err := cluster.New(path, cluster.Options{})
	if err != nil {
		t.Fatal("development cluster client unavailable")
	}
	db := sourceDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	bootstrap, err := db.Bootstrap(ctx, "query-api-mcp-fixture")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := db.Authenticate(ctx, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	makeKey := func(name string, write bool) (store.Key, string) {
		t.Helper()
		permissions := []string{"deployments:read", "databases:query"}
		if write {
			permissions = append(permissions, "databases:write-query")
		}
		key, token, e := db.CreateKey(ctx, owner, store.KeyInput{Name: name, Project: "demo", Environment: "development", Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		return key, token
	}
	readerKey, reader := makeKey("sql-reader", false)
	writerKey, writer := makeKey("sql-writer", true)
	entropy := make([]byte, 32)
	if _, err = rand.Read(entropy); err != nil {
		t.Fatal(err)
	}
	password := hex.EncodeToString(entropy)
	d := database.Resource{ID: store.NewID(), Project: "demo", Environment: "development", Revision: 1, Status: "ready", Spec: database.Spec{SchemaVersion: 1, Name: "sql-api-mcp-development-fixture", Engine: "postgresql", Version: "17", Mode: "standalone", Shards: 1, CPU: "250m", Memory: "512Mi", StorageGiB: 1, TLS: &database.TLSConfig{Mode: "required"}}}
	if nodes := os.Getenv("HAKOPOD_DATABASE_FIXTURE_NODES"); nodes != "" {
		for _, node := range strings.Split(nodes, ",") {
			if node != "k3d-hakopod-dev-server-0" && node != "k3d-hakopod-database-worker-0" && node != "k3d-hakopod-database-worker-1" {
				t.Fatal("SQL fixtures require a named development database node")
			}
			d.Spec.Placement.NodeNames = append(d.Spec.Placement.NodeNames, node)
		}
	}
	t.Log("owned SQL fixture namespace", cluster.DatabaseNamespace(d.ID))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 8*time.Minute)
		defer stop()
		for cleanup.Err() == nil {
			done, e := runtime.DeleteDatabase(cleanup, d, func() error { return cleanup.Err() })
			if e != nil {
				t.Error("owned SQL fixture cleanup failed")
				return
			}
			if done {
				t.Log("owned SQL fixture resources removed")
				return
			}
			time.Sleep(2 * time.Second)
		}
		t.Error("owned SQL fixture cleanup exceeded eight minutes")
	})
	if err = runtime.ApplyDatabase(ctx, d, []byte(password), func() error { return ctx.Err() }); err != nil {
		t.Fatal("owned PostgreSQL fixture creation failed", err)
	}
	ready := false
	for ctx.Err() == nil {
		observation, e := runtime.ObserveDatabase(ctx, d)
		if e == nil && observation.Status == "ready" {
			ready = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !ready {
		t.Fatal("owned PostgreSQL fixture did not become ready")
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO managed_databases(id,project,environment,name,revision,spec,status,credentials) VALUES($1,'demo','development',$2,1,$3,'ready',$4)`, d.ID, d.Spec.Name, store.JSON(d.Spec), []byte("DEVELOPMENT ONLY: credentials reside in the owned runtime secret")); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&Server{Store: db, Cluster: runtime}).Handler())
	defer server.Close()
	client := server.Client()
	client.Timeout = 30 * time.Second
	call := func(method, route, token, session string, body any) (int, http.Header, []byte) {
		t.Helper()
		encoded, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		r, e := http.NewRequestWithContext(ctx, method, server.URL+route, bytes.NewReader(encoded))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			r.Header.Set("Mcp-Session-Id", session)
			r.Header.Set("MCP-Protocol-Version", "2025-06-18")
		}
		response, e := client.Do(r)
		if e != nil {
			t.Fatal("SQL HTTP request failed")
		}
		defer response.Body.Close()
		content, e := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		if e != nil || len(content) > 2<<20 {
			t.Fatal("SQL HTTP response unavailable")
		}
		if bytes.Contains(content, []byte(password)) || bytes.Contains(content, []byte(writer)) || bytes.Contains(content, []byte(reader)) {
			t.Fatal("SQL response exposed a credential")
		}
		return response.StatusCode, response.Header, content
	}
	queryPath := "/api/v1/databases/" + d.ID + "/query"
	type auditExpectation struct{ key, statement, outcome string }
	operations := map[string]auditExpectation{}
	decodeResult := func(content []byte, key, statement, outcome string) database.QueryResult {
		t.Helper()
		var result database.QueryResult
		if err := json.Unmarshal(content, &result); err != nil || result.OperationID == "" || result.DatabaseID != d.ID {
			t.Fatal("SQL result lacks the operation or target identity")
		}
		operations[result.OperationID] = auditExpectation{key, statement, outcome}
		return result
	}
	const typedRead = "SELECT $1::numeric, NULL::text, current_setting('transaction_read_only')"
	status, _, body := call("POST", queryPath, reader, "", map[string]any{"sql": typedRead, "parameters": []any{json.Number("9007199254740993")}})
	if status != 200 {
		t.Fatal("canonical SQL read failed", status)
	}
	result := decodeResult(body, readerKey.ID, typedRead, "read")
	if len(result.Rows) != 1 || len(result.Rows[0]) != 3 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" || result.Rows[0][1] != nil || result.Rows[0][2] == nil || *result.Rows[0][2] != "on" {
		t.Fatal("canonical SQL read lost numeric, null or read-only state")
	}
	ddl := map[string]any{"sql": "CREATE TABLE api_mcp_fixture AS SELECT 0::numeric AS value, ''::text AS payload WHERE false", "read_only": false, "expected_revision": 1}
	status, _, _ = call("POST", queryPath, reader, "", ddl)
	if status != 404 {
		t.Fatal("reader reached SQL write execution", status)
	}
	ddl["expected_revision"] = 2
	status, _, _ = call("POST", queryPath, writer, "", ddl)
	if status != 409 {
		t.Fatal("stale SQL write review was accepted", status)
	}
	ddl["expected_revision"] = 1
	status, _, body = call("POST", queryPath, writer, "", ddl)
	if status != 200 || decodeResult(body, writerKey.ID, ddl["sql"].(string), "committed").Outcome != "committed" {
		t.Fatal("reviewed SQL schema write failed", status)
	}
	const mcpPath = "/api/v1/mcp?project=demo&environment=development&allow_sql=true"
	initialize := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "DEVELOPMENT ONLY SQL acceptance", "version": "1"}}}
	initSession := func(route, token string) string {
		t.Helper()
		status, headers, _ := call("POST", route, token, "", initialize)
		session := headers.Get("Mcp-Session-Id")
		if status != 200 || session == "" {
			t.Fatal("MCP initialization failed", status)
		}
		status, _, _ = call("POST", route, token, session, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
		if status != 202 {
			t.Fatal("MCP initialization notification failed", status)
		}
		return session
	}
	readSession := initSession(mcpPath, reader)
	writeSession := initSession(mcpPath+"&allow_sql_write=true", writer)
	tool := func(route, token, session string, arguments map[string]any, wantError bool) database.QueryResult {
		t.Helper()
		status, _, content := call("POST", route, token, session, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "database_query", "arguments": arguments}})
		var message struct {
			Result struct {
				IsError    bool            `json:"isError"`
				Structured json.RawMessage `json:"structuredContent"`
				Content    []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if status != 200 || json.Unmarshal(content, &message) != nil || message.Result.IsError != wantError {
			t.Fatal("MCP SQL outcome differs", status)
		}
		if wantError {
			if len(message.Result.Content) != 1 || message.Result.Content[0].Text != "SQL writes require --allow-sql-write" {
				t.Fatal("MCP write failed for a reason other than missing write consent")
			}
			return database.QueryResult{}
		}
		key, outcome := readerKey.ID, "read"
		if token == writer {
			key = writerKey.ID
		}
		if arguments["write"] == true {
			outcome = "committed"
		}
		var structured struct {
			Data               json.RawMessage `json:"data"`
			ContentIsUntrusted bool            `json:"content_is_untrusted"`
		}
		if json.Unmarshal(message.Result.Structured, &structured) != nil || !structured.ContentIsUntrusted || len(structured.Data) == 0 {
			t.Fatal("MCP SQL result lacks its untrusted data envelope")
		}
		return decodeResult(structured.Data, key, arguments["sql"].(string), outcome)
	}
	const payload = "api-mcp-private-fixture ' ; SELECT secret -- हिंदी"
	writeArguments := map[string]any{"database_id": d.ID, "sql": "INSERT INTO api_mcp_fixture VALUES ($1::numeric,$2::text) RETURNING value", "parameters": []any{json.Number("9007199254740993"), payload}, "write": true, "expected_revision": 1}
	tool(mcpPath, reader, readSession, writeArguments, true)
	result = tool(mcpPath+"&allow_sql_write=true", writer, writeSession, writeArguments, false)
	if result.Outcome != "committed" || len(result.Rows) != 1 || len(result.Rows[0]) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" {
		t.Fatal("MCP SQL write lost the exact bound value")
	}
	result = tool(mcpPath, reader, readSession, map[string]any{"database_id": d.ID, "sql": "SELECT value,payload,NULL FROM api_mcp_fixture"}, false)
	if result.Outcome != "read" || len(result.Rows) != 1 || len(result.Rows[0]) != 3 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" || result.Rows[0][1] == nil || *result.Rows[0][1] != payload || result.Rows[0][2] != nil {
		t.Fatal("MCP SQL readback differs")
	}
	for id, expected := range operations {
		var total, started, finished int
		hash := sha256.Sum256([]byte(expected.statement))
		if err = db.Pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE metadata->>'outcome'='started'), count(*) FILTER (WHERE metadata->>'outcome'=$4) FROM audit_events WHERE action='database.query' AND metadata->>'operation_id'=$1 AND resource=$2 AND key_id=$3 AND metadata->>'statement_sha256'=$5`, id, d.ID, expected.key, expected.outcome, hex.EncodeToString(hash[:])).Scan(&total, &started, &finished); err != nil || total != 2 || started != 1 || finished != 1 {
			t.Fatal("SQL operation audit identity or outcome differs")
		}
	}
	var exposed int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE metadata::text LIKE '%api-mcp-private-fixture%' OR metadata::text LIKE '%INSERT INTO api_mcp_fixture%' OR metadata::text LIKE $1", "%"+password+"%").Scan(&exposed); err != nil || exposed != 0 {
		t.Fatal("SQL audit exposed statement, parameters or credentials")
	}
	for _, token := range []string{bootstrap, reader, writer} {
		if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE position($1 in metadata::text)>0", token).Scan(&exposed); err != nil || exposed != 0 {
			t.Fatal("audit metadata exposed an API credential")
		}
	}
	status, _, _ = call("DELETE", mcpPath, reader, readSession, nil)
	if status != 204 {
		t.Fatal("MCP read session did not close", status)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE id=$1", writerKey.ID); err != nil {
		t.Fatal(err)
	}
	status, _, _ = call("POST", mcpPath+"&allow_sql_write=true", writer, writeSession, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/list"})
	if status != 401 {
		t.Fatal("revoked key retained MCP session access", status)
	}
	t.Log("Canonical HTTP and MCP preserved SQL grants, review, exact binds, readback, audits and revocation")
}
