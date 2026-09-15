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
	return p.CanManageProject(p.Project) && p.Allows("deployments:write", p.Project, p.Environment, "")
}
func (p Principal) CanManageApplication(project, environment, name string) bool {
	return p.Allows("deployments:write", project, environment, name) && (p.CanManageProject(project) ||
		(p.CredentialType == "machine" && p.Project == project && p.Environment == environment && project != "" && environment != "" && p.Application == "" && p.Allows("applications:manage", project, environment, name)))
}
func (p Principal) CanBindGit(project, environment string) bool {
	return p.CanManageGit() && p.Allows("deployments:write", project, environment, "")
}
