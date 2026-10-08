package spec

import "fmt"

// Applications select an alias. The installation owns the runtime and its grant.
func validateRuntimeProfile(s Service) error {
	if s.RuntimeProfile == "" {
		return nil
	}
	if !runtimeName.MatchString(s.RuntimeProfile) {
		return fmt.Errorf("runtime_profile: use an approved name with at most 63 lowercase letters, digits or hyphens")
	}
	if s.Actions != nil || s.Serverless != nil {
		return fmt.Errorf("runtime_profile is unavailable for Managed Actions and serverless services")
	}
	return nil
}
