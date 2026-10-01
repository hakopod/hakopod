package database

const MySQLSidecarCPU = "100m"
const MySQLSidecarMemory = "256Mi"
const MySQLRouterCPU = "100m"
const MySQLRouterMemory = "128Mi"

// Routing members run the native engine router. They do not store database
// data and must not be counted as voting or replicated database members.
type RoutingObservation struct {
	Kind    string   `json:"kind"`
	Ready   bool     `json:"ready"`
	Message string   `json:"message,omitempty"`
	Members []Member `json:"members"`
}

func (s Spec) RouterInstances() int {
	if s.Engine != "mysql" {
		return 0
	}
	if s.Mode == "standalone" {
		return 1
	}
	return 2
}
