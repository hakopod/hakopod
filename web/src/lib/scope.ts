import { createContext, useContext, useEffect } from 'react'
import type { Identity, Project } from './types'

export type Scope = {
  project: string
  environment: string
  identity: Identity
  can: (permission: string) => boolean
  syncScope: (project: string, environment: string) => void
}
export const ScopeContext = createContext<Scope | null>(null)

export function resolveWorkspaceScope(
  projects: Project[] | undefined,
  preferred: { project: string; environment: string },
) {
  const project = projects?.find((item) => item.name === preferred.project) || projects?.[0]
  const environment =
    project?.environments.find((item) => item.name === preferred.environment) ||
    project?.environments[0]
  return { project, environment: environment?.name || '' }
}

// Browser credentials carry a broad session envelope; project roles determine
// which controls are available. Go independently authorizes every request.
export function canAccess(identity: Identity, project: string, permission: string) {
  if (identity.mfa_required) return false
  if (identity.admin) return true
  if (identity.credential_type === 'browser') {
    const allowed: Record<string, string[]> = {
      admin: ['deployments:read', 'deployments:write', 'logs:read'],
      developer: ['deployments:read', 'deployments:write', 'logs:read'],
      viewer: ['deployments:read', 'logs:read'],
    }
    return Boolean(
      identity.project_roles?.some(
        (entry) =>
          entry.project === project &&
          (entry.permissions ?? allowed[entry.role])?.includes(permission),
      ),
    )
  }
  return identity.permissions.includes(permission)
}

export function canCreateEnvironment(identity: Identity, project: string) {
  return Boolean(
    project &&
    identity.credential_type === 'browser' &&
    (!identity.project || identity.project === project) &&
    !identity.environment &&
    !identity.application &&
    (identity.admin ||
      identity.project_roles?.some((entry) => entry.project === project && entry.role === 'admin')),
  )
}
export function useScope() {
  const context = useContext(ScopeContext)
  if (!context) throw new Error('Workspace context is unavailable. Reload the dashboard.')
  return context
}

export function useResourceScope(resource?: { project: string; environment: string }) {
  const scope = useScope()
  useEffect(() => {
    if (resource) scope.syncScope(resource.project, resource.environment)
  }, [resource?.project, resource?.environment, scope.syncScope])
}

export function canOpenHostTerminal(identity: Identity, node: string) {
  return (
    identity.credential_type === 'browser' &&
    Boolean(
      identity.owner ||
      identity.host_permissions?.some(
        (grant) =>
          grant.permission === 'nodes:terminal' && (grant.node === node || grant.node === '*'),
      ),
    )
  )
}
