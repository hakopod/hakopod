package cluster

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
)

func clickhouseKeeperFixtureFault(t *testing.T, ctx context.Context, d database.Resource, members []database.Member) func() {
	t.Helper()
	helper := os.Getenv("HAKOPOD_CLICKHOUSE_FAULT_HELPER")
	if helper == "" {
		t.Fatal("set HAKOPOD_CLICKHOUSE_FAULT_HELPER to the owned development quorum helper")
	}
	var stopped []database.Member
	resume := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, member := range stopped {
			if exec.CommandContext(cleanup, "python3", helper, "resume", d.ID, member.Name, member.UID).Run() != nil {
				t.Error("ClickHouse Keeper fixture could not resume", member.Name)
			}
		}
		stopped = nil
	}
	t.Cleanup(resume)
	for _, member := range members {
		stopped = append(stopped, member)
		if exec.CommandContext(ctx, "python3", helper, "pause", d.ID, member.Name, member.UID).Run() != nil {
			t.Fatal("ClickHouse Keeper fixture pause failed")
		}
	}
	return resume
}

func waitClickHouseObserved(t *testing.T, ctx context.Context, c *Client, d database.Resource) database.Observation {
	t.Helper()
	wait, stop := context.WithTimeout(ctx, 3*time.Minute)
	defer stop()
	var o database.Observation
	var err error
	for wait.Err() == nil {
		step, cancel := context.WithTimeout(wait, 40*time.Second)
		o, err = c.ObserveDatabase(step, d)
		cancel()
		if err == nil && o.Status == "ready" {
			return o
		}
		if sleepContext(wait, 3*time.Second) != nil {
			break
		}
	}
	t.Fatal("ClickHouse failed to recover native health", o.Message, err)
	return o
}

func TestManagedClickHouseKeeperQuorumLive(t *testing.T) {
	c, ctx := liveClickHouseClient(t)
	d, health := newClickHouseFixture(t, ctx, c, "cluster", 1)
	if health.Coordination == nil || len(health.Coordination.Members) != 3 {
		t.Fatal("three Keeper members are required")
	}
	for _, query := range []string{"CREATE TABLE app.quorum_fixture (id UInt64) ORDER BY id", "INSERT INTO app.quorum_fixture SETTINGS insert_quorum=2, insert_quorum_timeout=10000 VALUES (1)"} {
		if _, err := c.clickhouseQuery(ctx, d, health.Members[0], "app", query); err != nil {
			t.Fatal(err)
		}
	}
	var leader database.Member
	for _, m := range health.Coordination.Members {
		if m.Role == "leader" {
			leader = m
		}
	}
	if leader.UID == "" {
		t.Fatal("Keeper has no observed leader")
	}
	resume := clickhouseKeeperFixtureFault(t, ctx, d, []database.Member{leader})
	// Keeper and ClickHouse both use a 30-second session timeout. Allow the
	// surviving majority to expire the old session, elect and reconnect while
	// remaining below the independent 90-second rescue watchdog.
	step, stop := context.WithTimeout(ctx, 75*time.Second)
	// An election can interrupt an in-flight request. Retry one deduplicated
	// write within the recovery bound instead of assuming an error rolled back.
	var err error
	for {
		_, err = c.clickhouseQuery(step, d, health.Members[0], "app", "INSERT INTO app.quorum_fixture SETTINGS insert_quorum=2, insert_quorum_timeout=6000, insert_deduplication_token='keeper-leader-fixture' VALUES (2)")
		if err == nil || sleepContext(step, time.Second) != nil {
			break
		}
	}
	stop()
	resume()
	if err != nil {
		t.Fatal("ClickHouse could not write with a remaining Keeper majority", err)
	}
	health = waitClickHouseObserved(t, ctx, c, d)
	waitClickHouseData(t, ctx, c, d, health, "SELECT id FROM app.quorum_fixture ORDER BY id FORMAT TSV", "1\n2")
	resume = clickhouseKeeperFixtureFault(t, ctx, d, health.Coordination.Members[:2])
	if _, err = c.clickhouseQuery(ctx, d, health.Members[0], "app", "SELECT 1"); err != nil {
		resume()
		t.Fatal("data member itself became unreachable")
	}
	step, stop = context.WithTimeout(ctx, 12*time.Second)
	_, err = c.clickhouseQuery(step, d, health.Members[0], "app", "INSERT INTO app.quorum_fixture SETTINGS insert_quorum=2, insert_quorum_timeout=8000 VALUES (99)")
	stop()
	resume()
	if err == nil {
		t.Fatal("ClickHouse acknowledged a replicated write without Keeper quorum")
	}
	health = waitClickHouseObserved(t, ctx, c, d)
	// A timed-out write has an unknown outcome. Confirm previously acknowledged
	// rows without asserting that the unacknowledged probe was rolled back.
	waitClickHouseData(t, ctx, c, d, health, "SELECT id FROM app.quorum_fixture WHERE id IN (1,2) ORDER BY id FORMAT TSV", "1\n2")
	t.Log("Keeper leader loss retained acknowledged writes; loss of its majority prevented a write acknowledgment, and quorum recovered")
}
