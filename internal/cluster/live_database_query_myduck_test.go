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

func TestManagedMyDuckQueryLive(t *testing.T) {
	if os.Getenv("HAKOPOD_MYDUCK_QUERY_TEST") != "1" {
		t.Skip("set HAKOPOD_MYDUCK_QUERY_TEST=1 for owned development MyDuck query acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("MyDuck query acceptance requires k3d-hakopod-dev")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	var d database.Resource
	if reuse := os.Getenv("HAKOPOD_MYDUCK_QUERY_FIXTURE_ID"); reuse != "" {
		d = myduckFixture()
		d.ID, d.Spec.Name = reuse, "myduck-development-fixture"
		d.Spec.Placement.NodeNames = developmentRecoveryFixtureNodes(t)
		secret, e := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
		if e != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels[managedBy] != "hakopod" {
			t.Fatal("fixture ownership unavailable")
		}
		waitMyDuckFixture(t, ctx, c, d)
	} else {
		d, _, _ = newMyDuckFixture(t, ctx, c)
	}

	write := false
	query := func(statement string, parameters ...any) (database.QueryResult, error) {
		return queryMyDuckSQLFixture(ctx, c, d, database.QueryRequest{SQL: statement, Parameters: parameters, ReadOnly: &write, ExecutionMode: "nontransactional"})
	}
	_, err = query("CREATE TABLE hakopod_query_fixture(value BIGINT PRIMARY KEY, text_value VARCHAR)")
	if err != nil {
		t.Fatal("schema write", err)
	}
	payload := "quote' backslash\\ Unicode हिंदी ; DROP TABLE other --"
	result, err := query("INSERT INTO hakopod_query_fixture VALUES ($1,$2)", json.Number("9007199254740993"), payload)
	if err != nil || result.Outcome != "applied" {
		t.Fatal("bound write", err)
	}
	result, err = query("SELECT value,text_value,NULL FROM hakopod_query_fixture")
	if err != nil || len(result.Rows) != 1 || *result.Rows[0][0] != "9007199254740993" || *result.Rows[0][1] != payload || result.Rows[0][2] != nil {
		t.Fatal("bound readback", err)
	}
	if _, err = queryMyDuckSQLFixture(ctx, c, d, database.QueryRequest{SQL: "SELECT 1"}); err == nil {
		t.Fatal("readonly accepted")
	}
	if _, err = query("SELECT 1; INSERT INTO hakopod_query_fixture VALUES (2,'second')"); err == nil {
		t.Fatal("multiple statements accepted")
	}
	limited := database.QueryRequest{SQL: "SELECT * FROM range(10)", ReadOnly: &write, ExecutionMode: "nontransactional", MaxRows: 2}
	limited.Validate()
	if _, err = c.queryMyDuckSQL(ctx, d, limited, func(context.Context) error { return nil }); err == nil {
		t.Fatal("row bound accepted oversized result")
	}
	short, stop := context.WithTimeout(ctx, time.Second)
	busy := database.QueryRequest{SQL: "SELECT sum(i) FROM range(100000000000) t(i)", ReadOnly: &write, ExecutionMode: "nontransactional"}
	busy.Validate()
	_, err = c.queryMyDuckSQL(short, d, busy, func(context.Context) error { return nil })
	stop()
	if err == nil {
		t.Fatal("cancelled query accepted")
	}
	t.Log("MyDuck explicit write opt-in preserved PG binds and exact values")
}

func queryMyDuckSQLFixture(ctx context.Context, c *Client, d database.Resource, q database.QueryRequest) (database.QueryResult, error) {
	if err := q.Validate(); err != nil {
		return database.QueryResult{}, err
	}
	return c.queryMyDuckSQL(ctx, d, q, func(context.Context) error { return nil })
}

func TestMyDuckQueryFixtureCleanup(t *testing.T) {
	if os.Getenv("HAKOPOD_MYDUCK_QUERY_FIXTURE_ID") == "" {
		t.Skip("owned cleanup only")
	}
	c, err := New(os.Getenv("HAKOPOD_TEST_KUBECONFIG"), developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	d := myduckFixture()
	d.ID = os.Getenv("HAKOPOD_MYDUCK_QUERY_FIXTURE_ID")
	d.Spec.Name = "myduck-development-fixture"
	ctx, stop := context.WithTimeout(context.Background(), 8*time.Minute)
	defer stop()
	for ctx.Err() == nil {
		done, e := c.DeleteDatabase(ctx, d, func() error { return ctx.Err() })
		if e != nil {
			t.Fatal(e)
		}
		if done {
			return
		}
		sleepContext(ctx, 2*time.Second)
	}
	t.Fatal("owned MyDuck cleanup exceeded bound")
}
