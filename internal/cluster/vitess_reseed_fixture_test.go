package cluster

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
)

func TestVitessReseedFixtureBindsNumericNativeTabletTypes(t *testing.T) {
	d := database.Resource{ID: strings.Repeat("a", 32), Spec: database.Spec{Replicas: 2}}
	member := database.Member{Name: "tablet-replica", Shard: "-", Role: "replica"}
	hostname := member.Name + "." + DatabaseNamespace(d.ID) + ".svc.cluster.local"
	// The native GetTablets response uses JSON numbers for protobuf enums.
	value := fmt.Sprintf(`[
		{"alias":{"cell":"local","uid":11},"hostname":"tablet-primary","keyspace":"app","shard":"-","type":1},
		{"alias":{"cell":"local","uid":22},"hostname":%q,"keyspace":"app","shard":"-","type":2},
		{"alias":{"cell":"local","uid":33},"hostname":"another-replica","keyspace":"app","shard":"-","type":2}
	]`, hostname)
	alias, err := vitessFixtureReplicaAliasFromJSON(d, member, value)
	if err != nil || alias != "local-0000000022" {
		t.Fatalf("native replica binding = %q: %v", alias, err)
	}
	for name, invalid := range map[string]string{
		"string enum":  strings.ReplaceAll(value, `"type":2`, `"type":"REPLICA"`),
		"primary":      strings.ReplaceAll(value, `"type":2`, `"type":1`),
		"unknown enum": strings.ReplaceAll(value, `"type":1`, `"type":7`),
		"wrong shard":  strings.ReplaceAll(value, `"shard":"-"`, `"shard":"-80"`),
		"wrong host":   strings.ReplaceAll(value, hostname, "unobserved-tablet"),
		"duplicate":    strings.ReplaceAll(value, "another-replica", hostname),
		"incomplete":   `[]`,
		"invalid":      `{`,
	} {
		t.Run(name, func(t *testing.T) {
			if alias, err := vitessFixtureReplicaAliasFromJSON(d, member, invalid); err == nil || alias != "" {
				t.Fatalf("unsafe native replica binding = %q: %v", alias, err)
			}
		})
	}
}
