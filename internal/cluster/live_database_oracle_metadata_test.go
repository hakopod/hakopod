//go:build hakopod_native_acceptance

package cluster

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	goora "github.com/sijms/go-ora/v3"
	"k8s.io/client-go/tools/clientcmd"
	"os"
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
