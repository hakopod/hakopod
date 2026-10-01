package database

const ClickHouseKeeperCPU = "250m"
const ClickHouseKeeperMemory = "256Mi"
const ClickHouseKeeperStorageGiB int64 = 1

// Coordination members hold replication metadata, never application table data.
type CoordinationObservation struct {
	Ready   bool     `json:"ready"`
	Message string   `json:"message,omitempty"`
	Members []Member `json:"members"`
}

func (s Spec) KeeperInstances() int {
	if s.Engine == "clickhouse" && s.Mode == "cluster" {
		return 3
	}
	return 0
}
