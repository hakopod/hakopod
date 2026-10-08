//go:build hakopod_native_acceptance

package cluster

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"github.com/hakopod/hakopod/internal/database"
	goora "github.com/sijms/go-ora/v3"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"strings"
	"testing"
	"time"
)

func TestManagedOracleStaticMetadataLive(t *testing.T) {
	if os.Getenv("HAKOPOD_ORACLE_STATIC_METADATA_TEST") != "1" {
		t.Skip("explicit development metadata gate required")
	}
	if os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") != "" || os.Getenv("HAKOPOD_ORACLE_FIXTURE_ID") != "" {
		t.Fatal("metadata requires fresh fixture and normal cleanup")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("metadata development context")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal("metadata client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	d, observed := newOracleFixture(t, ctx, c, "")
	if len(observed.Members) != 1 || !observed.Members[0].Ready || observed.Members[0].UID == "" {
		t.Fatal("metadata requires exactly one ready owned Oracle member")
	}
	if _, _, err = c.databaseExecTarget(ctx, d, observed.Members[0]); err != nil {
		t.Fatal("metadata member ownership validation failed")
	}
	for _, probe := range []struct {
		name, statement string
		args            []driver.NamedValue
	}{
		{"untyped_null", "SELECT NULL FROM dual", nil},
		{"number_null", "SELECT CAST(:1 AS NUMBER(30,0)), NULL FROM dual", []driver.NamedValue{{Ordinal: 1, Value: "9007199254740993"}}},
		{"typed_null", "SELECT CAST(NULL AS NUMBER(30,0)) FROM dual", nil},
	} {
		func() {
			step, stop := context.WithTimeout(ctx, 20*time.Second)
			defer stop()
			native, e := c.oracleApplicationConnectionOptions(step, d, observed.Members[0], true, true)
			if e != nil {
				t.Fatal("metadata connection")
			}
			defer native.Close()
			conn, e := native.Conn(step)
			if e != nil {
				t.Fatal("metadata session")
			}
			defer conn.Close()
			if boundOracleQueryConnection(conn) != nil {
				t.Fatal("metadata decoder limit")
			}
			var events []goora.NativeDiagnosticEvent
			e = conn.Raw(func(raw any) error {
				actual, ok := raw.(*goora.Connection)
				if !ok {
					return driver.ErrBadConn
				}
				events = goora.NativeQueryMetadata(step, actual, probe.statement, probe.args)
				return nil
			})
			if e != nil {
				t.Fatal("metadata native access")
			}
			encoded, e := json.Marshal(events)
			if e != nil || len(encoded) > 2048 {
				t.Fatal("metadata output bound")
			}
			t.Logf("Oracle static probe=%s events=%s", probe.name, encoded)
		}()
	}
}

func TestSQLDiagnosticRecorderPrivacyAndBound(t *testing.T) {
	r := &sqlDiagnosticRecorder{started: time.Now()}
	ctx := context.WithValue(context.Background(), sqlDiagnosticKey{}, r)
	for i := 0; i < 100; i++ {
		sqlQueryDiagnosticStage(ctx, "private SQL", errors.New("private backend"))
	}
	data, err := json.Marshal(r.events)
	if err != nil || len(r.events) != 24 || strings.Contains(string(data), "private") || len(data) > 8192 {
		t.Fatal("diagnostic recorder privacy or bound failed")
	}
}
func TestManagedOracleAdapterStagesLive(t *testing.T) {
	if os.Getenv("HAKOPOD_ORACLE_ADAPTER_STAGES_TEST") != "1" {
		t.Skip("explicit development stage gate required")
	}
	if os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") != "" || os.Getenv("HAKOPOD_ORACLE_FIXTURE_ID") != "" {
		t.Fatal("stages require fresh fixture and normal cleanup")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("stage development context")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal("stage client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	d, observed := newOracleFixture(t, ctx, c, "")
	if len(observed.Members) != 1 || !observed.Members[0].Ready || observed.Members[0].UID == "" {
		t.Fatal("stages require exactly one ready owned member")
	}
	write := false
	q := database.QueryRequest{SQL: "SELECT CAST(:1 AS NUMBER(30,0)), NULL FROM dual", Parameters: []any{json.Number("9007199254740993")}, ReadOnly: &write, ExecutionMode: "nontransactional"}
	if q.Validate() != nil {
		t.Fatal("stage request")
	}
	for _, mode := range []string{"adapter", "direct", "driver_direct"} {
		func() {
			r := &sqlDiagnosticRecorder{started: time.Now()}
			recorded := context.WithValue(ctx, sqlDiagnosticKey{}, r)
			step, stop := context.WithTimeout(recorded, 20*time.Second)
			defer stop()
			var probeErr error
			if mode == "adapter" {
				_, probeErr = queryOracleSQLFixture(step, c, d, q)
			} else {
				native, e := c.oracleApplicationConnectionOptions(step, d, observed.Members[0], true, true)
				sqlQueryDiagnosticStage(step, "connector", e)
				if e == nil {
					defer native.Close()
					conn, e := native.Conn(step)
					sqlQueryDiagnosticStage(step, "connection", e)
					if e == nil {
						defer conn.Close()
						e = boundOracleQueryConnection(conn)
						sqlQueryDiagnosticStage(step, "decoder_limit", e)
						if e == nil {
							if mode == "driver_direct" {
								var events []goora.NativeDiagnosticEvent
								cause := "other"
								e = conn.Raw(func(raw any) error {
									actual, ok := raw.(*goora.Connection)
									if !ok {
										return driver.ErrBadConn
									}
									events, cause = goora.NativeQueryMetadataCause(step, actual, q.SQL, []driver.NamedValue{{Ordinal: 1, Value: string(q.Parameters[0].(json.Number))}})
									return nil
								})
								metadata, encodeErr := json.Marshal(events)
								if encodeErr != nil || len(metadata) > 2048 {
									t.Fatal("driver metadata output bound")
								}
								t.Logf("Oracle driver direct cause=%s events=%s", cause, metadata)
								if e == nil && cause != "none" {
									e = sqlDiagnosticStaticError(cause)
								}
							} else {
								_, e = runSQLDriverQuery(step, conn, "oracle", q, "read", func(context.Context) error { return nil })
							}
						}
					}
					probeErr = e
				} else {
					probeErr = e
				}
			}
			sqlQueryDiagnosticStage(step, "terminal", probeErr)
			encoded, e := json.Marshal(r.events)
			if e != nil || len(encoded) > 8192 {
				t.Fatal("stage output bound")
			}
			t.Logf("Oracle stages mode=%s events=%s", mode, encoded)
		}()
	}

}

func TestSQLDiagnosticCauseAllowlist(t *testing.T) {
	for _, scenario := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "deadline"}, {context.Canceled, "cancelled"}, {errors.New("TTC error: received code 3 during response reading"), "ttc_response_code_3"}, {errors.New("closed connection"), "closed_connection"}, {errors.New("private backend"), "other"},
	} {
		if sqlDiagnosticCause(scenario.err) != scenario.want {
			t.Fatal("diagnostic cause classification differs")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &sqlDiagnosticRecorder{started: time.Now()}
	ctx = context.WithValue(ctx, sqlDiagnosticKey{}, r)
	sqlQueryDiagnosticStage(ctx, "terminal", errors.New("private backend"))
	if len(r.events) != 1 || !r.events[0].ContextExpired || r.events[0].Cause != "other" {
		t.Fatal("diagnostic context replaced actual cause")
	}
}

func TestSQLDiagnosticStaticCausePreserved(t *testing.T) {
	for _, scenario := range []struct{ cause, want string }{{"ttc_response_code_3", "ttc_response_code_3"}, {"private SQL", "other"}, {"none", "other"}} {
		err := sqlDiagnosticStaticError(scenario.cause)
		if sqlDiagnosticCause(err) != scenario.want {
			t.Fatal("static failure cause was lost")
		}
		r := &sqlDiagnosticRecorder{started: time.Now()}
		ctx := context.WithValue(context.Background(), sqlDiagnosticKey{}, r)
		sqlQueryDiagnosticStage(ctx, "terminal", err)
		encoded, e := json.Marshal(r.events)
		if e != nil || len(r.events) != 1 || r.events[0].Cause == "none" || strings.Contains(string(encoded), "private") {
			t.Fatal("failed probe terminal classification was lost or disclosed text")
		}
	}
}
