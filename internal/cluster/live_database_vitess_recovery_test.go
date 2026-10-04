package cluster

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestManagedVitessRecoveryLive(t *testing.T) {
	fixtures, ctx := newVitessLiveClient(t, 40*time.Minute)
	c := fixtures.c
	source, password := newVitessFixture(t, ctx, fixtures, "recovery-source", 2)
	health := waitVitessFixture(t, ctx, c, source, password)
	client := vitessFixtureClient(t, ctx, c, source, health, password, "app@primary", 0)
	seedVitessFixture(t, ctx, client)
	archive := &databaseBoundedWriter{limit: 8 << 20}
	if err := c.DumpDatabase(ctx, source, health, archive); err != nil {
		t.Fatal("Vitess archive capture failed", err)
	}
	if _, err := client.ExecContext(ctx, "INSERT INTO records (id, payload, label) VALUES (99,0xCAFE,'source-only')"); err != nil {
		t.Fatal(err)
	}
	target, targetPassword := newVitessFixture(t, ctx, fixtures, "recovery-target", 2)
	target.Status, target.Recovery = "restoring", &database.Recovery{JobID: source.ID}
	before := waitVitessFixture(t, ctx, c, target, targetPassword)
	// Corruption is rejected before any SQL executes, including corruption of
	// the final shard after earlier shards have already reached staging files.
	broken := append([]byte(nil), archive.Bytes()...)
	broken[len(broken)-1] ^= 1
	for _, input := range [][]byte{broken, archive.Bytes()[:archive.Len()/2]} {
		if err := c.RestoreVitessDatabase(ctx, target, before, bytes.NewReader(input)); err == nil {
			t.Fatal("Vitess recovery accepted an invalid archive")
		}
		if err := c.DatabaseEmpty(ctx, target, before); err != nil {
			t.Fatal("Vitess invalid archive changed its target")
		}
	}
	prior := vitessFixtureClient(t, ctx, c, target, before, targetPassword, "app@primary", 0)
	stale, err := prior.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	if err := stale.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.RestoreVitessDatabase(ctx, target, before, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal("Vitess archive recovery failed", err)
	}
	probe, stop := context.WithTimeout(ctx, 5*time.Second)
	err = stale.PingContext(probe)
	stop()
	if err == nil {
		t.Fatal("Vitess recovery kept an existing gateway session alive")
	}
	after := waitVitessFixture(t, ctx, c, target, targetPassword)
	recovered := vitessFixtureClient(t, ctx, c, target, after, targetPassword, "app@primary", 0)
	checkVitessFixtureData(t, ctx, recovered)
	checkVitessShardRouting(t, ctx, c, target, after)
	var count int
	if err := client.QueryRowContext(ctx, "SELECT COUNT(*) FROM records WHERE id=99").Scan(&count); err != nil || count != 1 {
		t.Fatal("Vitess recovery changed the source")
	}
	if err := recovered.QueryRowContext(ctx, "SELECT COUNT(*) FROM records WHERE id=99").Scan(&count); err != nil || count != 0 {
		t.Fatal("Vitess recovery exceeded the captured data")
	}
	if _, err := recovered.ExecContext(ctx, "UPDATE records SET label='target-only' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	var label string
	if err := client.QueryRowContext(ctx, "SELECT label FROM records WHERE id=1").Scan(&label); err != nil || label != "नमस्ते / 東京" {
		t.Fatal("Vitess target writes changed source data")
	}
	if err := c.RestoreVitessDatabase(ctx, target, after, bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("Vitess recovery accepted a nonempty target")
	}
	testVitessRecoveryIngress(t, ctx, c, target)
	t.Log("Vitess recovered binary and Unicode rows on both shards, rejected corrupted input, revoked gateway sessions and retained recovery inspection gates")
}

func testVitessRecoveryIngress(t *testing.T, ctx context.Context, c *Client, target database.Resource) {
	t.Helper()
	check := func(want bool) {
		t.Helper()
		policy, err := c.kube.NetworkingV1().NetworkPolicies(DatabaseNamespace(target.ID)).Get(ctx, "database-vitess-clients", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		open := false
		for _, rule := range policy.Spec.Ingress {
			for _, peer := range rule.From {
				if peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels["hakopod.io/database-access-"+target.ID] == "true" {
					open = true
				}
			}
		}
		if open != want {
			t.Fatal("Vitess gateway access disagrees with durable recovery inspection")
		}
	}
	check(false)
	stamp := time.Now().UTC()
	target.Status, target.Recovery.RestoredAt = "ready", &stamp
	if err := c.ReconcileDatabaseNetworkPolicy(ctx, target, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	check(false)
	target.Recovery.InspectedAt = &stamp
	if err := c.ReconcileDatabaseNetworkPolicy(ctx, target, func() error { return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	check(true)
}
