package database

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

type RedisNode struct {
	ID, Host, Role, Primary string
	Slots                   []string
}

// ParseRedisTopology rejects partial coverage, duplicate slot ownership,
// disconnected members and in-flight migration. A count of 16384 alone cannot
// establish that every slot has exactly one owner.
func ParseRedisTopology(raw string, shards, replicas int) ([]RedisNode, string, error) {
	if len(raw) > 128<<10 || shards < 3 || shards > 16 || replicas < 1 || replicas > 2 {
		return nil, "", fmt.Errorf("Redis topology exceeds supported bounds")
	}
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) != shards*(1+replicas) || len(lines) > MaxMembers {
		return nil, "", fmt.Errorf("Redis member count does not match the requested topology")
	}
	slots := [16384]bool{}
	nodes := []RedisNode{}
	masters := map[string]int{}
	seen := map[string]bool{}
	canonical := []string{}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 8 || len(fields[0]) != 40 {
			return nil, "", fmt.Errorf("Redis returned an invalid cluster member")
		}
		if _, err := hex.DecodeString(fields[0]); err != nil || seen[fields[0]] {
			return nil, "", fmt.Errorf("Redis node identity is invalid or duplicated")
		}
		seen[fields[0]] = true
		if fields[7] != "connected" {
			return nil, "", fmt.Errorf("Redis has a disconnected cluster member")
		}
		flags := "," + fields[2] + ","
		role := "replica"
		if strings.Contains(flags, ",fail,") || strings.Contains(flags, ",fail?,") || strings.Contains(flags, ",handshake,") || strings.Contains(flags, ",noaddr,") {
			return nil, "", fmt.Errorf("Redis reports an unhealthy cluster member")
		}
		if strings.Contains(flags, ",master,") {
			role = "primary"
		} else if !strings.Contains(flags, ",slave,") {
			return nil, "", fmt.Errorf("Redis member role is unknown")
		}
		addr := strings.Split(strings.Split(fields[1], ",")[0], "@")[0]
		host, _, err := net.SplitHostPort(addr)
		if err != nil || net.ParseIP(host) == nil {
			return nil, "", fmt.Errorf("Redis member address is invalid")
		}
		node := RedisNode{ID: fields[0], Host: host, Role: role, Primary: fields[3]}
		if role == "primary" {
			if fields[3] != "-" || len(fields) < 9 {
				return nil, "", fmt.Errorf("Redis primary has no assigned slots")
			}
			masters[node.ID] = 0
			for _, span := range fields[8:] {
				if strings.ContainsAny(span, "[]") {
					return nil, "", fmt.Errorf("Redis slot migration is still in progress")
				}
				bounds := strings.Split(span, "-")
				if len(bounds) > 2 {
					return nil, "", fmt.Errorf("Redis slot range is invalid")
				}
				start, err := strconv.Atoi(bounds[0])
				if err != nil {
					return nil, "", fmt.Errorf("Redis slot range is invalid")
				}
				end := start
				if len(bounds) == 2 {
					end, err = strconv.Atoi(bounds[1])
				}
				if err != nil || start < 0 || end < start || end >= len(slots) {
					return nil, "", fmt.Errorf("Redis slot range is invalid")
				}
				for slot := start; slot <= end; slot++ {
					if slots[slot] {
						return nil, "", fmt.Errorf("Redis slot has more than one owner")
					}
					slots[slot] = true
				}
				node.Slots = append(node.Slots, span)
			}
		} else if len(fields) != 8 {
			return nil, "", fmt.Errorf("Redis replica unexpectedly owns slots")
		}
		nodes = append(nodes, node)
		canonical = append(canonical, strings.Join([]string{node.ID, node.Host, node.Role, node.Primary, strings.Join(node.Slots, ",")}, " "))
	}
	if len(masters) != shards {
		return nil, "", fmt.Errorf("Redis primary count does not match the requested shard count")
	}
	for _, node := range nodes {
		if node.Role == "replica" {
			count, ok := masters[node.Primary]
			if !ok {
				return nil, "", fmt.Errorf("Redis replica has no known primary")
			}
			masters[node.Primary] = count + 1
		}
	}
	for _, count := range masters {
		if count != replicas {
			return nil, "", fmt.Errorf("Redis shard does not have the requested replicas")
		}
	}
	for _, assigned := range slots {
		if !assigned {
			return nil, "", fmt.Errorf("Redis has unassigned slots")
		}
	}
	sort.Strings(canonical)
	fingerprint := sha256.Sum256([]byte(strings.Join(canonical, "\n")))
	return nodes, hex.EncodeToString(fingerprint[:]), nil
}
