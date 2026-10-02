package cluster

import (
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

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
