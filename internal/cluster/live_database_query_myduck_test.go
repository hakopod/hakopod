package cluster

import (
	"context"
	"encoding/json"
	"errors"
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
	if err != nil || len(result.Rows) != 1 || len(result.Rows[0]) != 3 || result.Rows[0][0] == nil || result.Rows[0][1] == nil || *result.Rows[0][0] != "9007199254740993" || *result.Rows[0][1] != payload || result.Rows[0][2] != nil {
		t.Fatal("bound readback", err)
	}
	if _, err = queryMyDuckSQLFixture(ctx, c, d, database.QueryRequest{SQL: "SELECT 1"}); err == nil {
		t.Fatal("readonly accepted")
	}
	if _, err = query("SELECT 1; INSERT INTO hakopod_query_fixture VALUES (2,'second')"); err == nil {
		t.Fatal("multiple statements accepted")
	}
	for _, limited := range []database.QueryRequest{{SQL: "SELECT * FROM range(10)", ReadOnly: &write, ExecutionMode: "nontransactional", MaxRows: 2}, {SQL: "SELECT repeat('x',2000)", ReadOnly: &write, ExecutionMode: "nontransactional", MaxBytes: 1024}} {
		limited.Validate()
		_, err = c.queryMyDuckSQL(ctx, d, limited, nil)
		var bounded *database.QueryError
		if !errors.As(err, &bounded) || bounded.Code != "database_query_result_limit" || bounded.Outcome != "unknown" {
			t.Fatal("bound outcome", err)
		}
	}
	oversized := database.QueryRequest{SQL: "SELECT repeat('x',2097152)", ReadOnly: &write, ExecutionMode: "nontransactional"}
	oversized.Validate()
	_, err = c.queryMyDuckSQL(ctx, d, oversized, nil)
	var protocolFailure *database.QueryError
	if !errors.As(err, &protocolFailure) || protocolFailure.Outcome != "unknown" {
		t.Fatal("oversized protocol value", err)
	}
	result, err = query("SELECT COUNT(*) FROM hakopod_query_fixture")
	if err != nil || len(result.Rows) != 1 || len(result.Rows[0]) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "1" {
		t.Fatal("negative probe persisted rows", err)
	}
	revoked := database.QueryRequest{SQL: "INSERT INTO hakopod_query_fixture VALUES (2,'revoked')", ReadOnly: &write, ExecutionMode: "nontransactional"}
	revoked.Validate()
	calls := 0
	_, err = c.queryMyDuckSQL(ctx, d, revoked, func(context.Context) error {
		calls++
		if calls > 1 {
			return context.Canceled
		}
		return nil
	})
	var authority *database.QueryError
	if !errors.As(err, &authority) || authority.Code != "database_query_authority_changed" || authority.Outcome != "unknown" || calls != 2 {
		t.Fatal("authority outcome", err, calls)
	}

	observation, e := c.ObserveDatabase(ctx, d)
	if e != nil || len(observation.Members) != 1 {
		t.Fatal("cancellation observation unavailable")
	}
	password, identity, e := c.myduckClientIdentity(ctx, d)
	if e != nil {
		t.Fatal("cancellation identity unavailable")
	}
	native, e := c.myduckMySQLClient(ctx, d, observation.Members[0], password, identity)
	if e != nil {
		t.Fatal("cancellation observer unavailable")
	}
	defer native.Close()
	cancelCtx, cancelQuery := context.WithCancel(ctx)
	defer cancelQuery()
	cancelled := make(chan error, 1)
	const marker = "hakopod_myduck_cancel_after_start"
	go func() {
		_, executionErr := queryMyDuckSQLFixture(cancelCtx, c, d, database.QueryRequest{SQL: "SELECT '" + marker + "', SUM(SIN(i::DOUBLE)) FROM range(100000000) t(i)", ReadOnly: &write, ExecutionMode: "nontransactional"})
		cancelled <- executionErr
	}()
	started := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		select {
		case earlyErr := <-cancelled:
			t.Fatalf("query finished before execution marker: error_type=%T", earlyErr)
		default:
		}
		var running int
		e = native.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID() AND INFO LIKE ?", "%"+marker+"%").Scan(&running)
		if e != nil {
			t.Fatal("observe executing query unavailable")
		}
		if running > 0 {
			started = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !started {
		t.Fatal("query never reached observed server execution")
	}
	select {
	case earlyErr := <-cancelled:
		t.Fatalf("query finished after marker before cancellation: error_type=%T", earlyErr)
	default:
	}
	cancelQuery()
	select {
	case e = <-cancelled:
		var interrupted *database.QueryError
		if !errors.As(e, &interrupted) || interrupted.Outcome != "unknown" {
			t.Fatal("in-flight cancellation outcome", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight cancellation exceeded five seconds")
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
