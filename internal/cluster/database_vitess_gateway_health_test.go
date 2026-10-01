package cluster

import (
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func TestVitessGatewayRequiresOwnedServingTablets(t *testing.T) {
	d := vitessTestDatabase()
	hostname := "tablet." + DatabaseNamespace(d.ID) + ".svc.cluster.local"
	members := []vitessGatewayMember{{Member: database.Member{Name: "tablet", UID: "uid", Shard: "-", Role: "primary", Ready: true}, Hostname: hostname, Alias: "local-0396507461"}}
	good := vitessGatewayTablet{Cell: "local", Keyspace: "app", Shard: "-", Role: "PRIMARY", State: "SERVING", Alias: "local-0396507461", Hostname: hostname}
	if !vitessGatewayTabletsMatch(d, members, []vitessGatewayTablet{good}) {
		t.Fatal("rejected exact native serving tablet")
	}
	for _, change := range []func(*vitessGatewayTablet){
		func(row *vitessGatewayTablet) { row.State = "NOT_SERVING" },
		func(row *vitessGatewayTablet) { row.Hostname = "foreign.invalid" },
		func(row *vitessGatewayTablet) { row.Role = "replica" },
		func(row *vitessGatewayTablet) { row.Shard = "wrong" },
		func(row *vitessGatewayTablet) { row.Alias = "local-0000000001" },
		func(row *vitessGatewayTablet) { row.Hostname = "10.42.1.105" },
	} {
		row := good
		change(&row)
		if vitessGatewayTabletsMatch(d, members, []vitessGatewayTablet{row}) {
			t.Fatal("accepted stale or foreign gateway view")
		}
	}
	if vitessGatewayTabletsMatch(d, members, nil) || vitessGatewayTabletsMatch(d, members, []vitessGatewayTablet{good, good}) {
		t.Fatal("accepted incomplete or oversized gateway view")
	}
}
