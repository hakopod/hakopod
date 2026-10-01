package cluster

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/hakopod/hakopod/internal/database"
)

type vitessGatewayTablet struct {
	Cell, Keyspace, Shard, Role, State, Alias, Hostname, PrimaryTerm string
}

type vitessGatewayMember struct {
	database.Member
	Hostname, Alias string
}

func vitessGatewayTabletsMatch(d database.Resource, members []vitessGatewayMember, rows []vitessGatewayTablet) bool {
	if len(rows) != len(members) || len(members) != d.Spec.Members() {
		return false
	}
	expected := make(map[string]vitessGatewayMember, len(members))
	for _, member := range members {
		host := member.Hostname
		wantHost := member.Name + "." + DatabaseNamespace(d.ID) + ".svc.cluster.local"
		if host != wantHost || member.Alias == "" || member.Name == "" || !member.Ready || expected[host].Name != "" {
			return false
		}
		expected[host] = member
	}
	seen := make(map[string]bool, len(rows))
	aliases := make(map[string]bool, len(rows))
	for _, row := range rows {
		member, ok := expected[row.Hostname]
		if !ok || seen[row.Hostname] || aliases[row.Alias] || row.Cell != "local" || row.Keyspace != "app" || row.Shard != member.Shard || row.Role != strings.ToUpper(member.Role) || row.State != "SERVING" || row.Alias != member.Alias {
			return false
		}
		seen[row.Hostname] = true
		aliases[row.Alias] = true
	}
	return true
}

func (c *Client) verifyVitessGatewayTablets(ctx context.Context, client *sql.DB, d database.Resource, members []database.Member) error {
	owned := make([]vitessGatewayMember, 0, len(members))
	for _, member := range members {
		pod, container, err := c.vitessExecTarget(ctx, d, member)
		if err != nil || container != "vttablet" {
			return fmt.Errorf("Vitess gateway tablet ownership changed")
		}
		uid, err := strconv.ParseUint(pod.Labels["planetscale.com/tablet-uid"], 10, 32)
		if err != nil || uid == 0 {
			return fmt.Errorf("Vitess gateway tablet alias is invalid")
		}
		// The operator emits one canonical hostname flag. Both the gateway view
		// and MySQL replication must use the owned tablet Service hostname.
		owned = append(owned, vitessGatewayMember{Member: member, Hostname: pod.Name + "." + pod.Namespace + ".svc.cluster.local", Alias: fmt.Sprintf("local-%010d", uid)})
	}
	rows, err := client.QueryContext(ctx, "SHOW VITESS_TABLETS")
	if err != nil {
		return fmt.Errorf("Vitess gateway tablet health is unavailable")
	}
	defer rows.Close()
	columns, err := rows.Columns()
	want := []string{"Cell", "Keyspace", "Shard", "TabletType", "State", "Alias", "Hostname", "PrimaryTermStartTime"}
	if err != nil || len(columns) != len(want) {
		return fmt.Errorf("Vitess gateway tablet health shape changed")
	}
	for i, column := range columns {
		if column != want[i] {
			return fmt.Errorf("Vitess gateway tablet health columns changed")
		}
	}
	observed := make([]vitessGatewayTablet, 0, len(members))
	for rows.Next() {
		if len(observed) >= len(members) {
			return fmt.Errorf("Vitess gateway tablet inventory exceeds its bound")
		}
		var row vitessGatewayTablet
		if rows.Scan(&row.Cell, &row.Keyspace, &row.Shard, &row.Role, &row.State, &row.Alias, &row.Hostname, &row.PrimaryTerm) != nil {
			return fmt.Errorf("Vitess gateway tablet health is invalid")
		}
		observed = append(observed, row)
	}
	if rows.Err() != nil || !vitessGatewayTabletsMatch(d, owned, observed) {
		return fmt.Errorf("Vitess gateway has not observed every owned serving tablet")
	}
	return nil
}
