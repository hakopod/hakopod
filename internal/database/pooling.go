package database

import "fmt"

const PoolerCPU = "250m"
const PoolerMemory = "256Mi"

// Pooling keeps primary and replica routes explicit. PgBouncer reuses server
// connections; it does not infer read/write intent from arbitrary SQL.
type Pooling struct {
	Mode                 string `json:"mode" toml:"mode"`
	Instances            int    `json:"instances" toml:"instances"`
	MaxClientConnections int    `json:"max_client_connections" toml:"max_client_connections"`
	DefaultPoolSize      int    `json:"default_pool_size" toml:"default_pool_size"`
	ReadOnly             bool   `json:"read_only" toml:"read_only"`
}

func (s Spec) ValidatePooling() error {
	p := s.Pooling
	if p == nil {
		return nil
	}
	if s.Engine != "postgresql" || !s.TLSRequired() {
		return fmt.Errorf("pooling requires PostgreSQL with required TLS")
	}
	if p.Mode != "session" && p.Mode != "transaction" {
		return fmt.Errorf("pooling.mode must be session or transaction")
	}
	if p.Instances < 1 || p.Instances > 3 {
		return fmt.Errorf("pooling.instances must be between 1 and 3 per route")
	}
	if p.MaxClientConnections < 20 || p.MaxClientConnections > 2000 {
		return fmt.Errorf("pooling.max_client_connections must be between 20 and 2000 per instance")
	}
	if p.DefaultPoolSize < 1 || p.DefaultPoolSize > 20 {
		return fmt.Errorf("pooling.default_pool_size must be between 1 and 20 per instance")
	}
	if p.ReadOnly && s.Replicas == 0 {
		return fmt.Errorf("read-only pooling requires a PostgreSQL replica")
	}
	return nil
}

func (s Spec) PoolerRoutes() int {
	if s.Pooling == nil {
		return 0
	}
	if s.Pooling.ReadOnly {
		return 2
	}
	return 1
}

func (s Spec) PoolerInstances() int {
	if s.Pooling == nil {
		return 0
	}
	return s.Pooling.Instances * s.PoolerRoutes()
}

func (s Spec) PlacementDomains() int {
	if s.Pooling == nil {
		return max(s.Members(), s.KeeperInstances(), s.VitessTopologyMembers())
	}
	return max(s.Members(), s.Pooling.Instances)
}

type PoolingObservation struct {
	Ready   bool     `json:"ready"`
	Message string   `json:"message,omitempty"`
	Members []Member `json:"members"`
}
