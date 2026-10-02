package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func liveClickHouseClient(t *testing.T) (*Client, context.Context) {
	t.Helper()
	if os.Getenv("HAKOPOD_CLICKHOUSE_TEST") != "1" {
		t.Skip("set HAKOPOD_CLICKHOUSE_TEST=1 for named development cluster acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("ClickHouse acceptance requires k3d-hakopod-dev")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	// Multi-member startup and the later recovery or trust transition each need
	// a bounded convergence window on the shared development cluster.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	t.Cleanup(cancel)
	return c, ctx
}

// A failed native fixture can be retired through the normal owned-resource
// deletion path. The caller must explicitly name its development-only identity.
func TestManagedClickHouseCleanupLive(t *testing.T) {
	id := os.Getenv("HAKOPOD_CLICKHOUSE_CLEANUP_ID")
	if id == "" {
		t.Skip("no retained ClickHouse development fixture selected")
	}
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 16 {
		t.Fatal("invalid ClickHouse cleanup fixture ID")
	}
	c, parent := liveClickHouseClient(t)
	ctx, cancel := context.WithTimeout(parent, 4*time.Minute)
	defer cancel()
	d := clickhouseFixture()
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
	t.Fatal("ClickHouse fixture deletion did not reclaim owned resources")
}

func newClickHouseFixture(t *testing.T, ctx context.Context, c *Client, mode string, shards int, reuseIDs ...string) (database.Resource, database.Observation) {
	t.Helper()
	d := clickhouseFixture()
	d.Spec.Mode, d.Spec.Shards = mode, shards
	if mode == "cluster" {
		d.Spec.Replicas = 1
	}
	id, password := make([]byte, 16), make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(password); err != nil {
		t.Fatal(err)
	}
	d.ID = hex.EncodeToString(id)
	password = []byte(hex.EncodeToString(password))
	if len(reuseIDs) != 0 && reuseIDs[0] != "" {
		decoded, err := hex.DecodeString(reuseIDs[0])
		if err != nil || len(decoded) != 16 {
			t.Fatal("invalid retained ClickHouse fixture ID")
		}
		d.ID = reuseIDs[0]
		secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
		if err != nil || secret.Labels[databaseOwner] != d.ID || secret.Labels["hakopod.io/project"] != d.Project || secret.Labels["hakopod.io/environment"] != d.Environment {
			t.Fatal("retained ClickHouse fixture ownership changed")
		}
		password = secret.Data["password"]
		object, err := c.dynamic.Resource(clickhouseDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(ctx, "database", metav1.GetOptions{})
		if err != nil || object.GetLabels()[databaseOwner] != d.ID {
			t.Fatal("retained ClickHouse controller ownership changed")
		}
		revision, err := strconv.ParseInt(object.GetAnnotations()["hakopod.io/database-revision"], 10, 64)
		if err != nil || revision < 1 {
			t.Fatal("retained ClickHouse revision is invalid")
		}
		d.Revision = revision + 1
	}
	t.Log("Development ClickHouse namespace", DatabaseNamespace(d.ID))
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Minute)
		defer stop()
		for cleanup.Err() == nil {
			done, err := c.DeleteDatabase(cleanup, d, func() error { return cleanup.Err() })
			if err != nil {
				t.Error(err)
				return
			}
			if done {
				return
			}
			if sleepContext(cleanup, 2*time.Second) != nil {
				break
			}
		}
		t.Error("ClickHouse deletion did not reclaim all owned volumes")
	})
	return d, waitClickHouseFixture(t, ctx, c, d, password)
}

func waitClickHouseData(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation, query, expected string) {
	t.Helper()
	for _, m := range o.Members {
		deadline := time.Now().Add(60 * time.Second)
		for {
			out, err := c.clickhouseQuery(ctx, d, m, "app", query)
			if err == nil && strings.TrimSpace(out) == expected {
				break
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				t.Fatal("ClickHouse replica data did not converge", m.Name, err)
			}
			if sleepContext(ctx, time.Second) != nil {
				t.Fatal(ctx.Err())
			}
		}
	}
}

func TestManagedClickHouseRecoveryLive(t *testing.T) {
	for _, layout := range []struct {
		name, mode string
		shards     int
	}{{"standalone", "standalone", 1}, {"cluster", "cluster", 1}, {"multishard", "cluster", 2}} {
		t.Run(layout.name, func(t *testing.T) {
			mode := layout.mode
			c, ctx := liveClickHouseClient(t)
			source, health := newClickHouseFixture(t, ctx, c, mode, layout.shards, os.Getenv("HAKOPOD_CLICKHOUSE_RECOVERY_SOURCE_ID"))
			engine := ""
			if mode == "standalone" {
				engine = " ENGINE=MergeTree"
			}
			for _, query := range []string{
				"DROP TABLE IF EXISTS app.recovery_distributed SYNC",
				"DROP TABLE IF EXISTS app.recovery_fixture SYNC",
				"CREATE TABLE app.recovery_fixture (id UInt64, value String)" + engine + " ORDER BY id",
			} {
				if _, err := c.clickhouseQuery(ctx, source, health.Members[0], "app", query); err != nil {
					t.Fatal(err)
				}
			}
			clickhouseInsertEachShard(t, ctx, c, source, health, 7, "unhex('0080ff0d0a')")
			waitClickHouseShardData(t, ctx, c, source, health, []int{7}, true)
			if layout.shards == 2 {
				if _, err := c.clickhouseQuery(ctx, source, health.Members[0], "app", "CREATE TABLE app.recovery_distributed AS app.recovery_fixture ENGINE=Distributed(managed, app, recovery_fixture, id % 2)"); err != nil {
					t.Fatal("could not create the Distributed recovery fixture", err)
				}
				waitClickHouseData(t, ctx, c, source, health, "SELECT id,hex(value) FROM app.recovery_distributed ORDER BY id FORMAT TSV", "7\t0080FF0D0A\n107\t0080FF0D0A")
			}
			var err error
			health, err = c.ObserveDatabase(ctx, source)
			if err != nil || health.Status != "ready" {
				t.Fatal("source replication did not become ready", err)
			}
			archive := &databaseBoundedWriter{limit: 16 << 20}
			if err = c.DumpDatabase(ctx, source, health, archive); err != nil {
				t.Fatal("native ClickHouse backup failed", err)
			}
			clickhouseInsertEachShard(t, ctx, c, source, health, 8, "'source-only'")
			target, targetHealth := newClickHouseFixture(t, ctx, c, mode, layout.shards, os.Getenv("HAKOPOD_CLICKHOUSE_RECOVERY_TARGET_ID"))
			target.Status, target.Recovery = "restoring", &database.Recovery{JobID: source.ID}
			if err = c.databaseNetworkPolicy(ctx, target, func() error { return ctx.Err() }); err != nil {
				t.Fatal(err)
			}
			if err = c.RestoreClickHouseDatabase(ctx, target, targetHealth, bytes.NewReader(archive.Bytes()[:archive.Len()/2])); err == nil {
				t.Fatal("truncated ClickHouse archive was accepted")
			}
			if err = c.DatabaseEmpty(ctx, target, targetHealth); err != nil {
				t.Fatal("incomplete archive changed target data", err)
			}
			if recoveryApplicationIngress(t, c, target) {
				t.Fatal("incomplete recovery opened client ingress")
			}
			if err = c.RestoreClickHouseDatabase(ctx, target, targetHealth, bytes.NewReader(archive.Bytes())); err != nil {
				t.Fatal("native ClickHouse restore failed", err)
			}
			targetHealth = waitClickHouseObserved(t, ctx, c, target)
			waitClickHouseShardData(t, ctx, c, target, targetHealth, []int{7}, true)
			if err = c.RestoreClickHouseDatabase(ctx, target, targetHealth, bytes.NewReader(archive.Bytes())); err == nil {
				t.Fatal("nonempty recovery target was accepted")
			}
			clickhouseInsertEachShard(t, ctx, c, target, targetHealth, 9, "'target-only'")
			waitClickHouseShardData(t, ctx, c, target, targetHealth, []int{7, 9}, false)
			waitClickHouseShardData(t, ctx, c, source, health, []int{7, 8}, false)
			if layout.shards == 2 {
				// The restored Distributed table must use the target's cluster,
				// not the source's members or stale replication paths.
				query := "SELECT id FROM app.recovery_distributed ORDER BY id FORMAT TSV"
				waitClickHouseData(t, ctx, c, target, targetHealth, query, "7\n9\n107\n109")
				waitClickHouseData(t, ctx, c, source, health, query, "7\n8\n107\n108")
			}
			for _, instance := range []struct {
				d database.Resource
				o database.Observation
			}{{source, health}, {target, targetHealth}} {
				for _, member := range instance.o.Members {
					// An unmatched glob remains literal. Any staged archive,
					// including one left by the invalid-input check, fails here.
					command := []string{"bash", "-c", `for file in /var/lib/clickhouse/backups/hakopod-*.zip; do if [ -e "$file" ]; then exit 1; fi; done`}
					if err := c.DatabaseExec(ctx, instance.d, member, command, nil, nil); err != nil {
						t.Fatal("ClickHouse recovery retained a staging archive", err)
					}
				}
			}
			testRecoveryIngressGates(t, ctx, c, target)
			t.Log("Native backup and isolated restore preserved binary data, rejected incomplete and nonempty recovery, and retained independent replication")
		})
	}
}

// Every shard contains different data. This detects restoring only the first
// shard, sharing replication paths with the source, and accidental duplication.
func clickhouseInsertEachShard(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation, id int, value string) {
	t.Helper()
	members, err := clickhouseShardMembers(d, o)
	if err != nil {
		t.Fatal(err)
	}
	for shard, m := range members {
		deadline := time.Now().Add(45 * time.Second)
		for {
			out, e := c.clickhouseQuery(ctx, d, m, "monitor", "SELECT count() FROM system.tables WHERE database='app' AND name='recovery_fixture' FORMAT TSV")
			err = e
			if err == nil && strings.TrimSpace(out) == "1" {
				break
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				t.Fatal("ClickHouse shard table did not appear", shard, err)
			}
			if sleepContext(ctx, time.Second) != nil {
				t.Fatal(ctx.Err())
			}
		}
		if _, err = c.clickhouseQuery(ctx, d, m, "app", fmt.Sprintf("INSERT INTO app.recovery_fixture VALUES (%d, %s)", shard*100+id, value)); err != nil {
			t.Fatal("ClickHouse shard insert failed", shard, err)
		}
	}
}

func waitClickHouseShardData(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation, ids []int, binary bool) {
	t.Helper()
	for shard := 0; shard < d.Spec.Shards; shard++ {
		part := o
		part.Members = nil
		for _, m := range o.Members {
			if m.Shard == fmt.Sprint(shard) {
				part.Members = append(part.Members, m)
			}
		}
		if len(part.Members) != d.Spec.Replicas+1 {
			t.Fatal("ClickHouse shard inventory is incomplete")
		}
		rows := []string{}
		for _, id := range ids {
			row := fmt.Sprint(shard*100 + id)
			if binary {
				row += "\t0080FF0D0A"
			}
			rows = append(rows, row)
		}
		query := "SELECT id FROM app.recovery_fixture ORDER BY id FORMAT TSV"
		if binary {
			query = "SELECT id,hex(value) FROM app.recovery_fixture ORDER BY id FORMAT TSV"
		}
		waitClickHouseData(t, ctx, c, d, part, query, strings.Join(rows, "\n"))
	}
}
