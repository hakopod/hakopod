package cluster

import (
	"context"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"testing"
	"time"
)

func TestManagedClickHouseQueryLive(t *testing.T) {
	if os.Getenv("HAKOPOD_CLICKHOUSE_QUERY_TEST") != "1" {
		t.Skip("set HAKOPOD_CLICKHOUSE_QUERY_TEST=1 for owned development ClickHouse SQL acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("ClickHouse SQL acceptance requires k3d-hakopod-dev")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	d, observed := newRecoveryFixtureConfigured(t, ctx, c, "clickhouse", "26.3", func(s *database.Spec) {
		s.TLS = &database.TLSConfig{Mode: "required"}
		s.CPU = "500m"
		s.Memory = "2Gi"
		s.StorageGiB = 2
		s.Name = "clickhouse-query-development-fixture"
	})
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal("fixture credentials unavailable")
	}
	observed = waitClickHouseFixture(t, ctx, c, d, secret.Data["password"])
	if observed.Status != "ready" {
		t.Fatal("fixture did not become ready")
	}
	query := func(q database.QueryRequest) (database.QueryResult, error) {
		if err := q.Validate(); err != nil {
			return database.QueryResult{}, err
		}
		// Retry only unavailable outcomes, which guarantee execution did not start.
		for attempt := 0; attempt < 3; attempt++ {
			result, err := c.queryClickHouseSQL(ctx, d, q, func(context.Context) error { return nil })
			if err == nil || err.Error() != "database_query_unavailable" {
				return result, err
			}
			if sleepContext(ctx, time.Second) != nil {
				return result, err
			}
		}
		return database.QueryResult{}, queryUnavailable()
	}
	result, err := query(database.QueryRequest{SQL: "SELECT {p1:UInt64}, NULL", Parameters: []any{json.Number("9007199254740993")}, ExecutionMode: "nontransactional"})
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" || result.Rows[0][1] != nil {
		t.Fatalf("typed result %#v %v", result, err)
	}
	write := false
	for _, statement := range []string{"CREATE TABLE app.hakopod_query_fixture(value UInt64) ENGINE=MergeTree ORDER BY value", "INSERT INTO app.hakopod_query_fixture VALUES (9007199254740993)"} {
		result, err = query(database.QueryRequest{SQL: statement, ReadOnly: &write, ExecutionMode: "nontransactional"})
		if err != nil || result.Outcome != "applied" {
			t.Fatal("explicit nontransactional write", err, result.Outcome)
		}
	}
	result, err = query(database.QueryRequest{SQL: "SELECT value FROM app.hakopod_query_fixture"})
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" {
		t.Fatal("write readback", err)
	}
	for _, statement := range []string{"INSERT INTO app.hakopod_query_fixture VALUES (2)", "DROP TABLE app.hakopod_query_fixture", "SET readonly=0", "SELECT 1 SETTINGS readonly=0", "SELECT 1; INSERT INTO app.hakopod_query_fixture VALUES (2)"} {
		if _, err = query(database.QueryRequest{SQL: statement}); err == nil {
			t.Fatalf("read-only statement accepted: %s", statement)
		}
	}
	// This bypasses the API classifier and tests ClickHouse's read-only boundary.
	if _, err = c.clickhouseQuery(ctx, d, observed.Members[0], "app", "INSERT INTO app.hakopod_query_fixture VALUES (2) SETTINGS readonly=1"); err == nil {
		t.Fatal("server read-only setting permitted an insert")
	}
	limited, err := query(database.QueryRequest{SQL: "SELECT number FROM numbers(5)", MaxRows: 2})
	if err != nil || len(limited.Rows) != 2 || !limited.Truncated {
		t.Fatal("row bound", err)
	}
	short, stop := context.WithTimeout(ctx, time.Second)
	q := database.QueryRequest{SQL: "SELECT sleep(3)"}
	if err = q.Validate(); err != nil {
		t.Fatal(err)
	}
	_, err = c.queryClickHouseSQL(short, d, q, func(context.Context) error { return nil })
	stop()
	if err == nil {
		t.Fatal("cancellation returned success")
	}
	t.Log("ClickHouse native app identity retained typed parameters and explicit nontransactional writes")
}
