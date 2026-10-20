package spec

import "fmt"

// Applications select an alias. The installation owns what it grants: a relaxed
// pod security profile that only makes sense on one dedicated, pinned node.
func validateNodeProfile(s Service) error {
	if s.NodeProfile == "" {
		return nil
	}
	if !runtimeName.MatchString(s.NodeProfile) {
		return fmt.Errorf("node_profile: use an approved name with at most 63 lowercase letters, digits or hyphens")
	}
	if s.Actions != nil || s.Serverless != nil {
		return fmt.Errorf("node_profile is unavailable for Managed Actions and serverless services")
	}
	// A profiled service is a singleton on one node: nothing else may run it
	// twice or move it, and a restart must stop the old pod before the new one.
	if s.NodeName == "" {
		return fmt.Errorf("node_profile requires node_name: the grant is tied to one dedicated node")
	}
	if s.Replicas > 1 || s.Autoscaling != nil {
		return fmt.Errorf("node_profile requires a single replica without autoscaling")
	}
	return nil
}
