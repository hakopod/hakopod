package database

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Vitess controls the application routing schema. Infrastructure partitions
// alone do not tell vtgate which shard owns a row.
type VitessConfig struct {
	Tables                    []VitessTable `json:"tables,omitempty" toml:"tables"`
	BackupDestinationID       string        `json:"backup_destination_id" toml:"backup_destination_id"`
	BackupDestinationRevision int64         `json:"backup_destination_revision" toml:"backup_destination_revision"`
}

type VitessTable struct {
	Name           string `json:"name" toml:"name"`
	ShardingColumn string `json:"sharding_column" toml:"sharding_column"`
}

const (
	VitessVersion                      = "23"
	VitessTabletCPU                    = "100m"
	VitessTabletMemory                 = "256Mi"
	VitessGatewayCPU                   = "250m"
	VitessGatewayMemory                = "256Mi"
	VitessControlCPU                   = "500m"
	VitessControlMemory                = "256Mi"
	VitessTopologyCPU                  = "100m"
	VitessTopologyMemory               = "256Mi"
	VitessTopologyStorageGiB     int64 = 1
	VitessOperatorCPU                  = "100m"
	VitessOperatorMemory               = "256Mi"
	VitessBackupControllerCPU          = "100m"
	VitessBackupControllerMemory       = "128Mi"
	// Two multipart buffers for a 1 TiB file can use 210 MiB. The backup
	// process also needs room for compression and its ordinary Go heap.
	VitessBackupMemory = "512Mi"
	VitessMaxTables    = 128
)

var vitessIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// ValidateVitess is independent of Spec.Validate so capability checks can use
// this contract before allowing a controller to create any resources.
func (s Spec) ValidateVitess() error {
	if s.Vitess == nil || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(s.Vitess.BackupDestinationID) || s.Vitess.BackupDestinationRevision < 1 {
		return fmt.Errorf("Vitess requires an approved native backup destination ID and revision for member recovery")
	}
	return s.validateVitessShape()
}

func (s Spec) validateVitessShape() error {
	if s.Engine != "vitess" || s.Version != VitessVersion || !s.TLSRequired() {
		return fmt.Errorf("Vitess requires version 23 and required TLS")
	}
	if s.Mode == "standalone" {
		if s.Shards != 1 || s.Replicas != 0 {
			return fmt.Errorf("standalone Vitess requires one shard and no replicas")
		}
	} else if s.Mode == "cluster" {
		if !slices.Contains([]int{1, 2, 4, 8}, s.Shards) || s.Replicas < 1 || s.Replicas > 5 || s.Members() > MaxMembers {
			return fmt.Errorf("Vitess clusters require 1, 2, 4 or 8 shards and 1–5 replicas per shard, with at most 48 tablets")
		}
	} else {
		return fmt.Errorf("Vitess mode must be standalone or cluster")
	}
	cpu, err := resource.ParseQuantity(s.CPU)
	if err != nil || cpu.Cmp(resource.MustParse("500m")) < 0 || cpu.Cmp(resource.MustParse("16")) > 0 {
		return fmt.Errorf("Vitess requires 500m–16 CPU per MySQL member, plus tablet and control resources")
	}
	memory, err := resource.ParseQuantity(s.Memory)
	if err != nil || memory.Cmp(resource.MustParse("1Gi")) < 0 || memory.Cmp(resource.MustParse("64Gi")) > 0 {
		return fmt.Errorf("Vitess requires 1–64Gi memory per MySQL member, plus tablet and control resources")
	}
	if s.StorageGiB < 1 || s.StorageGiB > 1024 {
		return fmt.Errorf("Vitess storage must be 1–1024Gi per MySQL member")
	}
	if s.Vitess == nil {
		if s.Shards > 1 {
			return fmt.Errorf("sharded Vitess requires an explicit table routing schema")
		}
		return nil
	}
	if len(s.Vitess.Tables) > VitessMaxTables || (s.Shards > 1 && len(s.Vitess.Tables) == 0) {
		return fmt.Errorf("sharded Vitess requires 1–128 table routing entries")
	}
	if s.Shards == 1 && len(s.Vitess.Tables) != 0 {
		return fmt.Errorf("single-shard Vitess does not use table sharding columns")
	}
	seen := make(map[string]bool, len(s.Vitess.Tables))
	for _, table := range s.Vitess.Tables {
		if !vitessIdentifier.MatchString(table.Name) || !vitessIdentifier.MatchString(table.ShardingColumn) || seen[table.Name] {
			return fmt.Errorf("Vitess table names and sharding columns must be unique valid SQL identifiers")
		}
		seen[table.Name] = true
	}
	return nil
}

// VitessVSchema supports a single hash vindex for an integer routing column.
// No user-supplied SQL, credentials or operator flags enter this document.
func (s Spec) VitessVSchema() ([]byte, error) {
	if err := s.validateVitessShape(); err != nil {
		return nil, err
	}
	if s.Shards == 1 {
		return []byte(`{"sharded":false}`), nil
	}
	tables := make(map[string]any, len(s.Vitess.Tables))
	for _, table := range s.Vitess.Tables {
		tables[table.Name] = map[string]any{"column_vindexes": []any{map[string]any{"column": table.ShardingColumn, "name": "hash"}}}
	}
	return json.Marshal(map[string]any{"sharded": true, "vindexes": map[string]any{"hash": map[string]any{"type": "hash"}}, "tables": tables})
}

func (s Spec) VitessGateways() int {
	if s.Engine != "vitess" {
		return 0
	}
	if s.Mode == "standalone" {
		return 1
	}
	return 2
}

// The pinned controller creates one vtorc per shard and one vtctld per cluster.
func (s Spec) VitessOrchestrators() int {
	if s.Engine != "vitess" {
		return 0
	}
	return s.Shards
}

func (s Spec) VitessTopologyMembers() int {
	if s.Engine != "vitess" {
		return 0
	}
	return 3
}

func (s Spec) VitessShardNames() []string {
	if s.Engine != "vitess" || !slices.Contains([]int{1, 2, 4, 8}, s.Shards) {
		return nil
	}
	if s.Shards == 1 {
		return []string{"-"}
	}
	names := make([]string, s.Shards)
	for i := range names {
		start, end := "", ""
		if i != 0 {
			start = fmt.Sprintf("%02x", 256*i/s.Shards)
		}
		if i+1 != s.Shards {
			end = fmt.Sprintf("%02x", 256*(i+1)/s.Shards)
		}
		names[i] = start + "-" + end
	}
	return names
}
