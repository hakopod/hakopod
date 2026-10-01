package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func oracleFixture() database.Resource {
	return database.Resource{ID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", Project: "demo", Environment: "development", Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "oracle-development-fixture", Engine: "oracle", Version: "23.26", Mode: "standalone", Shards: 1, CPU: "1", Memory: "4Gi", StorageGiB: 10, TLS: &database.TLSConfig{Mode: "required"}, Oracle: &database.OracleConfig{Edition: "free"}}}
}

func liveOracleClient(t *testing.T) (*Client, context.Context) {
	t.Helper()
	if os.Getenv("HAKOPOD_ORACLE_TEST") != "1" {
		t.Skip("set HAKOPOD_ORACLE_TEST=1 for named development cluster acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("Oracle acceptance requires k3d-hakopod-dev")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Minute)
	t.Cleanup(cancel)
	return c, ctx
}

func newOracleFixture(t *testing.T, ctx context.Context, c *Client, reuse string) (database.Resource, database.Observation) {
	t.Helper()
	var err error
	d := oracleFixture()
	d.Spec.Placement.NodeNames = []string{"k3d-hakopod-dev-server-0"}
	id, password := make([]byte, 16), make([]byte, 32)
	if _, err = rand.Read(id); err != nil {
		t.Fatal(err)
	}
	if _, err = rand.Read(password); err != nil {
		t.Fatal(err)
	}
	d.ID = hex.EncodeToString(id)
	password = []byte(hex.EncodeToString(password))
	if reuse != "" {
		if value, e := hex.DecodeString(reuse); e != nil || len(value) != 16 {
			t.Fatal("invalid Oracle fixture ID")
		}
		d.ID = reuse
		secret, e := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
		if e != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels["hakopod.io/project"] != d.Project || secret.Labels["hakopod.io/environment"] != d.Environment {
			t.Fatal("Oracle fixture ownership changed")
		}
		password = secret.Data["password"]
		object, e := c.dynamic.Resource(oracleDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
		if e != nil || object.GetLabels()[databaseOwner] != d.ID {
			t.Fatal("Oracle workload ownership changed")
		}
		revision, e := strconv.ParseInt(object.GetAnnotations()["hakopod.io/database-revision"], 10, 64)
		if e != nil || revision < 1 {
			t.Fatal("invalid Oracle fixture revision")
		}
		d.Revision = revision + 1
	}
	t.Log("Development Oracle namespace", DatabaseNamespace(d.ID))
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 4*time.Minute)
		defer stop()
		for cleanup.Err() == nil {
			done, e := c.DeleteDatabase(cleanup, d, func() error { return cleanup.Err() })
			if e != nil {
				t.Error(e)
				return
			}
			if done {
				return
			}
			_ = sleepContext(cleanup, 2*time.Second)
		}
		t.Error("Oracle deletion did not reclaim all owned volumes")
	})
	var health database.Observation
	for ctx.Err() == nil {
		if err = c.ApplyDatabase(ctx, d, password, func() error { return ctx.Err() }); err == nil {
			health, err = c.ObserveDatabase(ctx, d)
			if err == nil && health.Status == "ready" {
				break
			}
		}
		t.Log("Waiting for Oracle", health.Message, err)
		if sleepContext(ctx, 5*time.Second) != nil {
			t.Fatal("Oracle readiness timed out")
		}
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if health.TLS == nil || !health.TLS.Verified || !health.TLS.PlaintextRejected || health.Primary == "" {
		t.Fatal("Oracle native identity or TLS enforcement was not verified")
	}
	return d, health
}

func TestManagedOracleFreeLive(t *testing.T) {
	c, ctx := liveOracleClient(t)
	d, health := newOracleFixture(t, ctx, c, os.Getenv("HAKOPOD_ORACLE_FIXTURE_ID"))
	client, err := c.oracleApplicationConnection(ctx, d, health.Members[0], true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, query := range []string{"CREATE TABLE managed_fixture (id NUMBER PRIMARY KEY, value VARCHAR2(100))", "INSERT INTO managed_fixture VALUES (7,'native')"} {
		if _, err = client.ExecContext(ctx, query); err != nil {
			t.Fatal("Oracle application write failed", err)
		}
	}
	var value string
	if err = client.QueryRowContext(ctx, "SELECT value FROM managed_fixture WHERE id=7").Scan(&value); err != nil || value != "native" {
		t.Fatal("Oracle application read failed", err)
	}
	for _, query := range []string{"CREATE USER attacker IDENTIFIED BY password", "SELECT * FROM sys.user$", "ALTER SYSTEM SET sessions=500 SCOPE=SPFILE"} {
		if _, err = client.ExecContext(ctx, query); err == nil {
			t.Fatal("Oracle application exceeded schema privileges")
		}
	}
	t.Log("Oracle Free native create, authenticated reads and writes, private TCPS and schema privilege boundaries passed")
}

// Retire only an explicitly selected owned development fixture, including a
// failed initialization whose data must not be silently reused.
func TestManagedOracleCleanupLive(t *testing.T) {
	id := os.Getenv("HAKOPOD_ORACLE_CLEANUP_ID")
	if id == "" {
		t.Skip("no retained Oracle development fixture selected")
	}
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 16 {
		t.Fatal("invalid Oracle cleanup fixture ID")
	}
	c, parent := liveOracleClient(t)
	ctx, cancel := context.WithTimeout(parent, 4*time.Minute)
	defer cancel()
	d := oracleFixture()
	d.ID = id
	for ctx.Err() == nil {
		done, err := c.DeleteDatabase(ctx, d, func() error { return ctx.Err() })
		if err != nil {
			t.Fatal(err)
		}
		if done {
			return
		}
		if sleepContext(ctx, 2*time.Second) != nil {
			break
		}
	}
	t.Fatal("Oracle fixture deletion did not reclaim owned resources")
}
