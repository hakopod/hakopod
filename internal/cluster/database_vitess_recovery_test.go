package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func TestVitessRecoveryWaitsForGatewayReplacementReadiness(t *testing.T) {
	before := database.Observation{Revision: 7, Status: "ready", Members: []database.Member{
		{Name: "tablet-a", UID: "uid-a", Shard: "-", Role: "primary", Ready: true},
		{Name: "tablet-b", UID: "uid-b", Shard: "-", Role: "replica", Ready: true},
	}}
	calls := 0
	next, err := waitVitessRecoveryTopology(context.Background(), before, func(context.Context) (database.Observation, error) {
		calls++
		if calls == 1 {
			pending := before
			pending.Status = "pending"
			return pending, errVitessGatewayNotReady
		}
		return before, nil
	})
	if err != nil || calls != 2 || next.Status != "ready" {
		t.Fatalf("gateway replacement readiness = %q after %d observations: %v", next.Status, calls, err)
	}
}

func TestVitessRecoveryReadinessDoesNotHideTopologyOrOwnershipChanges(t *testing.T) {
	before := database.Observation{Revision: 7, Status: "ready", Members: []database.Member{{Name: "tablet-a", UID: "uid-a", Shard: "-", Role: "primary", Ready: true}}}
	for _, name := range []string{"replacement", "missing", "revision", "ownership", "unexplained pending"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			_, err := waitVitessRecoveryTopology(context.Background(), before, func(context.Context) (database.Observation, error) {
				calls++
				next := before
				next.Members = append([]database.Member(nil), before.Members...)
				pending := errVitessGatewayNotReady
				switch name {
				case "replacement":
					next.Members[0].UID = "replacement"
				case "missing":
					next.Members = nil
				case "revision":
					next.Revision++
				case "ownership":
					pending = errors.New("database controller ownership changed")
				case "unexplained pending":
					next.Status, pending = "pending", nil
				}
				return next, pending
			})
			if err == nil || calls != 1 {
				t.Fatalf("unsafe recovery observation was retried: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestVitessRecoveryReadinessHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	before := database.Observation{Revision: 1, Members: []database.Member{{Name: "tablet-a", UID: "uid-a", Shard: "-"}}}
	calls := 0
	_, err := waitVitessRecoveryTopology(ctx, before, func(context.Context) (database.Observation, error) {
		calls++
		cancel()
		return before, errVitessGatewayNotReady
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("recovery readiness ignored cancellation: calls=%d err=%v", calls, err)
	}
}

func TestSameVitessTabletSetAllowsReparentButRejectsReplacement(t *testing.T) {
	before := []database.Member{
		{Name: "tablet-a", UID: "uid-a", Shard: "-80", Role: "primary"},
		{Name: "tablet-b", UID: "uid-b", Shard: "-80", Role: "replica"},
	}
	reparented := []database.Member{
		{Name: "tablet-b", UID: "uid-b", Shard: "-80", Role: "primary"},
		{Name: "tablet-a", UID: "uid-a", Shard: "-80", Role: "replica"},
	}
	if !sameVitessTabletSet(before, reparented) {
		t.Fatal("healthy reparent changed the tablet identity set")
	}
	replaced := append([]database.Member(nil), reparented...)
	replaced[0].UID = "replacement"
	if sameVitessTabletSet(before, replaced) {
		t.Fatal("tablet replacement was accepted during recovery")
	}
}

func TestVitessWaitForGTIDQueryUsesAValidatedHexLiteral(t *testing.T) {
	query, err := vitessWaitForGTIDQuery("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-42,\n bbbbbbbb-cccc-dddd-eeee-ffffffffffff:1-7")
	if err != nil || !strings.HasPrefix(query, "SELECT WAIT_FOR_EXECUTED_GTID_SET(CONVERT(0x") || !strings.HasSuffix(query, " USING utf8mb4),30)") || strings.Contains(query, "aaaaaaaa-bbbb") {
		t.Fatalf("unexpected GTID wait query: %q %v", query, err)
	}
	for _, invalid := range []string{"", "uuid:1'); SELECT 1; --", strings.Repeat("a", 64<<10+1)} {
		if _, err := vitessWaitForGTIDQuery(invalid); err == nil {
			t.Fatalf("invalid GTID set was accepted: %.32q", invalid)
		}
	}
}
