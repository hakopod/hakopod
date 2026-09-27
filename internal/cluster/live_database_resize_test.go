package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestManagedRedisResizeAndBackupLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_RESIZE_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_RESIZE_TEST=1 for named development cluster acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil || cfg.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("requires named development cluster")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(random)
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	// Provisioning and both controller-driven reshardings share this deadline.
	// The bounded development worker also rolls members during each change.
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Minute)
	defer stop()
	d := database.Resource{ID: id, Project: "demo", Environment: "development", Revision: 1, Spec: database.Spec{SchemaVersion: 1, Name: "managed-database-development-fixture", Engine: "redis", Version: "8", Mode: "cluster", Shards: 3, Replicas: 1, CPU: "100m", Memory: "128Mi", StorageGiB: 1}}
	t.Logf("development fixture namespace %s", DatabaseNamespace(id))
	t.Cleanup(func() {
		if os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") == "1" && t.Failed() {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		_, _ = c.DeleteDatabase(cleanup, d, func() error { return nil })
	})
	password := make([]byte, 32)
	if _, err = rand.Read(password); err != nil {
		t.Fatal(err)
	}
	if err = c.ApplyDatabase(ctx, d, []byte(hex.EncodeToString(password)), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	observed := waitManagedDatabase(t, ctx, c, d)
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || secret.Labels[databaseOwner] != id {
		t.Fatal("fixture credential ownership")
	}
	execute := func(script string) {
		t.Helper()
		input := append(append([]byte(nil), secret.Data["password"]...), '\n')
		command := []string{"sh", "-c", "set -eu; IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; " + script}
		if err := c.DatabaseExec(ctx, d, observed.Members[0], command, bytes.NewReader(input), io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	execute(`i=0; while [ "$i" -lt 60 ]; do redis-cli -c SET "development-fixture:$i" "value:$i" PX 3600000 >/dev/null; i=$((i+1)); done`)
	for _, shards := range []int{4, 3} {
		var archive databaseBoundedWriter
		archive.limit = 8 << 20
		if err = c.DumpDatabase(ctx, d, observed, &archive); err != nil {
			t.Fatal("snapshot", err)
		}
		count := 0
		_, err = database.ReadRedisArchive(bytes.NewReader(archive.Bytes()), func(_ database.RedisArchive, _ database.Member, r io.Reader) error {
			header := make([]byte, 5)
			if _, err := io.ReadFull(r, header); err != nil {
				return err
			}
			if string(header) != "REDIS" {
				t.Fatal("snapshot is not an RDB archive")
			}
			count++
			_, err := io.Copy(io.Discard, r)
			return err
		})
		if err != nil || count != d.Spec.Shards {
			t.Fatal("snapshot integrity", err)
		}
		t.Logf("captured and checked %d real shard snapshots", count)
		d.Revision++
		d.Spec.Shards = shards
		if err = c.ApplyDatabase(ctx, d, secret.Data["password"], func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		observed = waitManagedDatabase(t, ctx, c, d)
		execute(`i=0; while [ "$i" -lt 60 ]; do [ "$(redis-cli -c --raw GET "development-fixture:$i")" = "value:$i" ]; [ "$(redis-cli -c --raw PTTL "development-fixture:$i")" -gt 0 ]; i=$((i+1)); done`)
		t.Logf("verified values and expiry after resizing to %d shards", shards)
	}
}
