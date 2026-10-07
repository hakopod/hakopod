package operations

type Policy struct {
	FixedScope         map[string]string `json:"fixed_scope,omitempty"`
	Category           string            `json:"category"`
	CredentialRequired bool              `json:"credential_required,omitempty"`
	ScopeRequirement   string            `json:"scope_requirement,omitempty"`
	Boundary           string            `json:"boundary"`
	Exposure           string            `json:"exposure"`
	MachinePermissions []string          `json:"machine_permissions,omitempty"`
	Permissions        []string          `json:"permissions"`
	Review             string            `json:"review"`
	SensitiveFields    []string          `json:"sensitive_fields"`
	Prerequisite       string            `json:"prerequisite,omitempty"`
	References         map[string]string `json:"references,omitempty"`
	Resource           string            `json:"resource,omitempty"`
	ResourceParameter  string            `json:"resource_parameter,omitempty"`
}

func classify(op Operation) (Policy, string) {
	policy := op.Policy
	if policy.Category == "" || policy.Category == "unclassified" || policy.Exposure == "unavailable" {
		return Policy{Category: "unclassified", Boundary: "none", Exposure: "unavailable"}, "This contract operation is not classified. Update the OpenAPI policy before invocation."
	}
	if policy.Exposure == "installation" {
		return policy, "Requires a separate installation connection with admin and agent:admin."
	}
	if policy.Exposure == "generic" {
		return policy, ""
	}
	if policy.Prerequisite != "" {
		return policy, policy.Prerequisite
	}
	return policy, "This operation requires a dedicated workflow. Generic invocation is unavailable."
}
