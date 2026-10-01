package database

import "k8s.io/apimachinery/pkg/api/resource"

const OracleBrokerCPU = "100m"
const OracleBrokerMemory = "256Mi"

func (s Spec) OracleBrokerInstances() int {
	if s.Engine == "oracle" && s.Oracle != nil && s.Oracle.Edition == "enterprise" && s.Mode == "cluster" {
		return 1
	}
	return 0
}

// CPUReservationMilli includes supporting processes and replacement capacity.
// Hosted admission must reserve these resources along with the data members.
func (s Spec) CPUReservationMilli() (int64, error) {
	if err := s.Validate(); err != nil {
		return 0, err
	}
	total := cpuMilli(s.CPU) * int64(s.Members()+1)
	if s.PoolerInstances() > 0 {
		total += cpuMilli(PoolerCPU) * int64(s.PoolerInstances()+s.PoolerRoutes())
	}
	if s.Engine == "mysql" {
		total += cpuMilli(MySQLSidecarCPU) * int64(s.Members()+1)
		total += cpuMilli(MySQLRouterCPU) * int64(s.RouterInstances()+1)
	}
	if s.Engine == "mongodb" {
		total += 100 * int64(s.Members()+1)
	}
	if s.KeeperInstances() > 0 {
		total += cpuMilli(ClickHouseKeeperCPU) * int64(s.KeeperInstances()+1)
	}
	if s.Engine == "vitess" {
		total += cpuMilli(VitessTabletCPU) * int64(s.Members()+1)
		total += cpuMilli(VitessGatewayCPU) * int64(s.VitessGateways()+1)
		total += cpuMilli(VitessControlCPU) * int64(s.VitessOrchestrators()+2)
		total += cpuMilli(VitessTopologyCPU) * int64(s.VitessTopologyMembers()+1)
		total += cpuMilli(VitessOperatorCPU) * 2
		total += cpuMilli(VitessBackupControllerCPU)
		total += 2 * int64(s.Shards) * cpuMilli(s.CPU)
	}
	total += cpuMilli(OracleBrokerCPU) * int64(s.OracleBrokerInstances())
	return total, nil
}

func cpuMilli(value string) int64 {
	q := resource.MustParse(value)
	return q.MilliValue()
}
