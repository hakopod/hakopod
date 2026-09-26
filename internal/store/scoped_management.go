package store

// Scoped management never grants installation administration. Delegated keys
// must be explicitly issued for one entire project/environment.
func (p Principal) CanManageGit() bool {
	if p.IsAdmin() {
		return true
	}
	if p.Project == "" || p.Environment == "" || p.Application != "" {
		return false
	}
	if p.CredentialType == "machine" {
		return p.Allows("git:manage", p.Project, p.Environment, "")
	}
	return (p.CanManageProject(p.Project) || p.canManageCLIWorkload(p.Project)) && p.Allows("deployments:write", p.Project, p.Environment, "")
}

// CanManageDNSProviders governs the stored DNS provider credentials, shaped
// like CanManageGit: installation administrators always, otherwise a principal
// delegated one entire project/environment. A machine credential is refused
// because the permission set has no DNS permission to issue, and reading
// "git:manage" as permission to write DNS records would widen a key its holder
// requested for source access.
func (p Principal) CanManageDNSProviders() bool {
	if p.IsAdmin() {
		return true
	}
	if p.Project == "" || p.Environment == "" || p.Application != "" || p.CredentialType == "machine" {
		return false
	}
	return (p.CanManageProject(p.Project) || p.canManageCLIWorkload(p.Project)) && p.Allows("deployments:write", p.Project, p.Environment, "")
}

func (p Principal) CanManageApplication(project, environment, name string) bool {
	return p.Allows("deployments:write", project, environment, name) && (p.CanManageProject(project) || p.canManageCLIWorkload(project) ||
		(p.CredentialType == "machine" && p.Project == project && p.Environment == environment && project != "" && environment != "" && p.Application == "" && p.Allows("applications:manage", project, environment, name)))
}
func (p Principal) CanBindGit(project, environment string) bool {
	return p.CanManageGit() && p.Allows("deployments:write", project, environment, "")
}

// CLI consent delegates workload setup, never project membership administration.
func (p Principal) canManageCLIWorkload(project string) bool {
	if p.CredentialType != "cli" || p.Project != project || project == "" || p.Environment == "" || p.Application != "" || !p.Allows("deployments:write", project, p.Environment, "") {
		return false
	}
	if p.Admin {
		return true
	}
	for _, role := range p.ProjectRoles {
		if role.Project == project && role.Role == "admin" {
			return true
		}
	}
	return false
}
