package cluster

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// The acceptance client routes only to observed, owned primaries. A MOVED reply
// is a test failure until a fresh verified topology is obtained; no arbitrary
// server-supplied destination is dialed from the control plane.
func redisClusterAcceptanceClient(t *testing.T, ctx context.Context, c *Client, d database.Resource, o database.Observation) func(...[]byte) any {
	t.Helper()
	raw, err := c.redisCommand(ctx, d, o.Members[0], "CLUSTER NODES")
	if err != nil {
		t.Fatal(err)
	}
	nodes, fingerprint, err := database.ParseRedisTopology(raw, d.Spec.Shards, d.Spec.Replicas)
	if err != nil || fingerprint != o.TopologyFingerprint {
		t.Fatal("acceptance topology changed", err)
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var slots [16384]*database.RedisWire
	for _, node := range nodes {
		if node.Role != "primary" {
			continue
		}
		var member database.Member
		for _, candidate := range o.Members {
			if candidate.Role == "primary" && candidate.Shard == node.ID {
				member = candidate
				break
			}
		}
		if member.UID == "" {
			t.Fatal("unowned acceptance primary")
		}
		wire, closeStream, err := c.databaseRedisMemberConnection(ctx, d, member)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeStream)
		if _, err = wire.Command([]byte("AUTH"), secret.Data["password"]); err != nil {
			t.Fatal(err)
		}
		for _, span := range node.Slots {
			parts := strings.Split(span, "-")
			lo, _ := strconv.Atoi(parts[0])
			hi := lo
			if len(parts) == 2 {
				hi, _ = strconv.Atoi(parts[1])
			}
			for i := lo; i <= hi; i++ {
				slots[i] = wire
			}
		}
	}
	return func(args ...[]byte) any {
		t.Helper()
		if len(args) < 2 {
			t.Fatal("acceptance command requires a key")
		}
		wire := slots[database.RedisKeySlot(args[1])]
		if wire == nil {
			t.Fatal("acceptance key has no verified owner")
		}
		result, err := wire.Command(args...)
		if err != nil {
			t.Fatal(err)
		}
		if string(args[0]) == "SET" {
			acks, err := wire.Command([]byte("WAIT"), []byte("1"), []byte("5000"))
			if err != nil || acks != int64(1) {
				t.Fatal("fixture write did not reach its replica", err)
			}
		}
		return result
	}
}

func TestManagedRedisClusterTLSRecoveryAndPrimaryLossLive(t *testing.T) {
	c, ctx := liveRecoveryClient(t)
	source, health := newRecoveryFixture(t, ctx, c, "redis", "8", true)
	if !source.Spec.TLSRequired() {
		t.Skip("set HAKOPOD_DATABASE_TLS_TEST=1 for encrypted cluster acceptance")
	}
	command := redisClusterAcceptanceClient(t, ctx, c, source, health)
	value := []byte{0, 128, 255, '\r', '\n'}
	expires := time.Now().Add(12 * time.Minute).UnixMilli()
	keys := make([][]byte, 60)
	for i := range keys {
		keys[i] = append([]byte(fmt.Sprintf("cluster-fixture-%d-", i)), 0, 255)
		command([]byte("SET"), keys[i], value, []byte("PXAT"), []byte(strconv.FormatInt(expires, 10)))
	}
	archive := &databaseBoundedWriter{limit: 8 << 20}
	if err := c.DumpDatabase(ctx, source, health, archive); err != nil {
		t.Fatal(err)
	}
	var primary database.Member
	for _, m := range health.Members {
		if m.Role == "primary" {
			primary = m
			break
		}
	}
	if primary.UID == "" {
		t.Fatal("fixture primary absent")
	}
	if err := c.kube.CoreV1().Pods(DatabaseNamespace(source.ID)).Delete(ctx, primary.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: ptr(types.UID(primary.UID))}}); err != nil {
		t.Fatal(err)
	}
	if err := sleepContext(ctx, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	health = waitManagedDatabase(t, ctx, c, source)
	recovered := redisClusterAcceptanceClient(t, ctx, c, source, health)
	// Other shards can be idle during a large recovery; their transport must survive.
	if err := sleepContext(ctx, 40*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		got, ok := recovered([]byte("GET"), key).([]byte)
		if !ok || !bytes.Equal(got, value) {
			t.Fatal("replica-acknowledged fixture write was lost")
		}
	}
	target, targetHealth := newRecoveryFixture(t, ctx, c, "redis", "8", true)
	target.Status = "restoring"
	target.Recovery = &database.Recovery{JobID: source.ID}
	if err := c.RestoreRedisDatabase(ctx, target, targetHealth, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	targetHealth = waitManagedDatabase(t, ctx, c, target)
	restored := redisClusterAcceptanceClient(t, ctx, c, target, targetHealth)
	for _, key := range keys {
		got, ok := restored([]byte("GET"), key).([]byte)
		if !ok || !bytes.Equal(got, value) {
			t.Fatal("recovered clustered binary value changed")
		}
		if restored([]byte("PEXPIRETIME"), key) != expires {
			t.Fatal("recovered clustered absolute expiry changed")
		}
	}
	if targetHealth.TLS == nil || !targetHealth.TLS.Verified || !targetHealth.SlotsHealthy {
		t.Fatal("recovered TLS or slot health unverified")
	}
	t.Log("Verified six-member Redis TLS cluster, replica-acknowledged binary writes across shards, primary replacement, separate-target shard recovery and absolute expiry")
}
