package cluster

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManagedRedisClusterRecoveryLive(t *testing.T) {
	c, ctx := liveRecoveryClient(t)
	source, sourceHealth := newRecoveryFixture(t, ctx, c, "redis", "8")
	run := func(d database.Resource, o database.Observation, script string) string {
		t.Helper()
		secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		input := bytes.NewReader(append(append([]byte(nil), secret.Data["password"]...), '\n'))
		var out bytes.Buffer
		if err = c.DatabaseExec(ctx, d, o.Members[0], []string{"sh", "-c", "set -eu; IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; " + script}, input, &out); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out.String())
	}
	run(source, sourceHealth, `i=0; while [ "$i" -lt 90 ]; do redis-cli SET "cluster-recovery-fixture:$i" "value:$i" PX 900000 >/dev/null; i=$((i+1)); done`)
	archive := &databaseBoundedWriter{limit: 8 << 20}
	if err := c.DumpDatabase(ctx, source, sourceHealth, archive); err != nil {
		t.Fatal(err)
	}
	target, health := newRecoveryFixture(t, ctx, c, "redis", "8", true)
	target.Status = "restoring"
	target.Recovery = &database.Recovery{JobID: strings.Repeat("c", 32)}
	corrupted := append([]byte(nil), archive.Bytes()...)
	corrupted[len(corrupted)-1] ^= 1
	if err := c.RestoreRedisDatabase(ctx, target, health, bytes.NewReader(corrupted)); err == nil {
		t.Fatal("corrupt archive accepted")
	}
	if err := c.DatabaseEmpty(ctx, target, health); err != nil {
		t.Fatal("corrupt archive changed target", err)
	}
	// A failed recovery intentionally requires a fresh target. Remove this owned
	// fixture and create another rather than bypassing that product contract.
	cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
	_, _ = c.DeleteDatabase(cleanup, target, func() error { return nil })
	stop()
	target, health = newRecoveryFixture(t, ctx, c, "redis", "8", true)
	target.Status = "restoring"
	target.Recovery = &database.Recovery{JobID: strings.Repeat("d", 32)}
	if err := c.RestoreRedisDatabase(ctx, target, health, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal("cluster restore", err)
	}
	check := `i=0; while [ "$i" -lt 90 ]; do [ "$(redis-cli -c --raw GET "cluster-recovery-fixture:$i")" = "value:$i" ]; [ "$(redis-cli -c PTTL "cluster-recovery-fixture:$i")" -gt 0 ]; i=$((i+1)); done; printf verified`
	if run(target, health, check) != "verified" {
		t.Fatal("recovered values/expiry mismatch")
	}
	var replica database.Member
	for _, m := range health.Members {
		if m.Role == "replica" {
			replica = m
			break
		}
	}
	if replica.Name == "" {
		t.Fatal("no observed replica")
	}
	pod, err := c.kube.CoreV1().Pods(DatabaseNamespace(target.ID)).Get(ctx, replica.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, deleteOptions(pod)); err != nil {
		t.Fatal(err)
	}
	for ctx.Err() == nil {
		o, e := c.ObserveDatabase(ctx, target)
		replaced := false
		for _, m := range o.Members {
			if m.Name == replica.Name && m.UID != replica.UID {
				replaced = true
			}
		}
		if e == nil && o.Status == "ready" && replaced {
			health = o
			break
		}
		time.Sleep(time.Second)
	}
	if run(target, health, check) != "verified" {
		t.Fatal("replica replacement lost data")
	}
	clusterArchive := &databaseBoundedWriter{limit: 8 << 20}
	if err = c.DumpDatabase(ctx, target, health, clusterArchive); err != nil {
		t.Fatal("recovered cluster backup", err)
	}
	// Exercise every shard in the archive, including restarting the isolated
	// decoder between RDB files, while the source cluster remains available.
	copy, copyHealth := newRecoveryFixture(t, ctx, c, "redis", "8")
	copy.Status = "restoring"
	copy.Recovery = &database.Recovery{JobID: strings.Repeat("e", 32)}
	if err = c.RestoreRedisDatabase(ctx, copy, copyHealth, bytes.NewReader(clusterArchive.Bytes())); err != nil {
		t.Fatal("multi-shard archive restore", err)
	}
	if run(copy, copyHealth, check) != "verified" || run(target, health, check) != "verified" {
		t.Fatal("multi-shard recovery changed values, expiry or source")
	}
	if run(source, sourceHealth, "redis-cli DBSIZE") != "90" {
		t.Fatal("source changed")
	}
	t.Log(fmt.Sprintf("Verified 90 keys across %d shards, expiry, corrupt archive rejection, replica replacement and multi-shard archive recovery into a separate database", target.Spec.Shards))
}
