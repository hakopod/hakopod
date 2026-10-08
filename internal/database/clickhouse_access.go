package database

import "fmt"

// ClickHouseConfig selects application access at creation or reviewed activation.
// Tenant administration is restricted to a dedicated standalone instance.
type ClickHouseConfig struct {
	AccessProfile string `json:"access_profile" toml:"access_profile"`
}

func (s Spec) ClickHouseTenantAdmin() bool {
	return s.Engine == "clickhouse" && s.ClickHouse != nil && s.ClickHouse.AccessProfile == "tenant_admin"
}

func (s Spec) validateClickHouseAccess() error {
	if s.Engine != "clickhouse" {
		return fmt.Errorf("clickhouse configuration requires engine clickhouse")
	}
	switch s.ClickHouse.AccessProfile {
	case "application":
		return nil
	case "tenant_admin":
		if s.Mode != "standalone" {
			return fmt.Errorf("ClickHouse tenant_admin requires standalone mode; replicated access entities are not supported")
		}
		return nil
	default:
		return fmt.Errorf("clickhouse.access_profile must be application or tenant_admin")
	}
}

func (s Spec) clickHouseSameCapacity(other Spec) bool {
	s.ClickHouse, other.ClickHouse = nil, nil
	return s.Equal(other)
}
