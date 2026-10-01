package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func clickhouseFixture() database.Resource {
	return database.Resource{ID: strings.Repeat("c", 32), Project: "demo", Environment: "development", Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "clickhouse-development-fixture", Engine: "clickhouse", Version: "26.3", Mode: "standalone", Shards: 1, CPU: "500m", Memory: "2Gi", StorageGiB: 2, TLS: &database.TLSConfig{Mode: "required"}}}
}

func TestManagedClickHouseLive(t *testing.T) {
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
	for _, mode := range []string{"standalone", "cluster"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			d := clickhouseFixture()
			d.Spec.Mode = mode
			if mode == "cluster" {
				d.Spec.Replicas = 1
			}
			id, password := make([]byte, 16), make([]byte, 32)
			if _, err = rand.Read(id); err != nil {
				t.Fatal(err)
			}
			if _, err = rand.Read(password); err != nil {
				t.Fatal(err)
			}
			d.ID = hex.EncodeToString(id)
			password = []byte(hex.EncodeToString(password))
			if reuse := os.Getenv("HAKOPOD_CLICKHOUSE_FIXTURE_ID"); reuse != "" {
				if bytes, e := hex.DecodeString(reuse); e != nil || len(bytes) != 16 {
					t.Fatal("invalid ClickHouse fixture ID")
				}
				d.ID = reuse
				secret, e := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
				if e != nil || secret.Labels[databaseOwner] != d.ID {
					t.Fatal("ClickHouse fixture credentials are unavailable")
				}
				password = secret.Data["password"]
				if revision := os.Getenv("HAKOPOD_CLICKHOUSE_FIXTURE_REVISION"); revision != "" {
					d.Revision, e = strconv.ParseInt(revision, 10, 64)
					if e != nil || d.Revision < 1 {
						t.Fatal("invalid ClickHouse fixture revision")
					}
				}
			}
			t.Log("Development ClickHouse namespace", DatabaseNamespace(d.ID))
			t.Cleanup(func() {
				if t.Failed() && os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" {
					return
				}
				cleanup, stop := context.WithTimeout(context.Background(), 3*time.Minute)
				defer stop()
				for cleanup.Err() == nil {
					done, e := c.DeleteDatabase(cleanup, d, func() error { return nil })
					if e != nil {
						t.Error(e)
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
			o := waitClickHouseFixture(t, ctx, c, d, password)
			if o.TLS == nil || !o.TLS.Verified || !o.TLS.PlaintextRejected {
				t.Fatal("ClickHouse TLS enforcement is unverified")
			}
			if mode == "cluster" && (o.Coordination == nil || !o.Coordination.Ready || len(o.Coordination.Members) != 3) {
				t.Fatal("ClickHouse Keeper quorum is unverified")
			}
			name := "fixture_" + hex.EncodeToString(id)
			query := "CREATE TABLE app." + name + " (id UInt64, value String)"
			if mode == "standalone" {
				query += " ENGINE=MergeTree"
			}
			query += " ORDER BY id"
			if _, e := c.clickhouseQuery(ctx, d, o.Members[0], "app", query); e != nil {
				t.Fatal("application could not create a table", e)
			}
			if _, e := c.clickhouseQuery(ctx, d, o.Members[0], "app", "INSERT INTO app."+name+" VALUES (7,'native')"); e != nil {
				t.Fatal("application could not insert", e)
			}
			for _, m := range o.Members {
				deadline := time.Now().Add(45 * time.Second)
				for {
					out, e := c.clickhouseQuery(ctx, d, m, "app", "SELECT id,value FROM app."+name+" FORMAT TSV")
					if e == nil && strings.TrimSpace(out) == "7\tnative" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("ClickHouse data did not replicate", m.Name, e)
					}
					_ = sleepContext(ctx, time.Second)
				}
			}
			for _, query := range []string{"CREATE DATABASE forbidden", "CREATE USER attacker IDENTIFIED WITH no_password", "SELECT * FROM file('/etc/passwd','RawBLOB')", "SELECT * FROM system.users"} {
				if _, e := c.clickhouseQuery(ctx, d, o.Members[0], "app", query); e == nil {
					t.Fatal("ClickHouse application exceeded its database privileges")
				}
			}
			t.Log("Native create, authenticated query, member replication, TLS enforcement and privilege boundaries passed")
		})
	}
}

func waitClickHouseFixture(t *testing.T, ctx context.Context, c *Client, d database.Resource, password []byte) database.Observation {
	t.Helper()
	var o database.Observation
	var err error
	for ctx.Err() == nil {
		step, stop := context.WithTimeout(ctx, 40*time.Second)
		err = c.ApplyDatabase(step, d, password, func() error { return nil })
		if apierrors.IsInvalid(err) {
			stop()
			t.Fatal("ClickHouse controller rejected generated specification", err)
		}
		if err == nil {
			o, err = c.ObserveDatabase(step, d)
		}
		stop()
		if err == nil && o.Status == "ready" {
			return o
		}
		t.Log("Waiting for ClickHouse", o.Message, err)
		if sleepContext(ctx, 5*time.Second) != nil {
			break
		}
	}
	t.Fatal("ClickHouse did not become ready", err)
	return o
}
