import { createContext, useContext } from 'react'
import type { Identity } from './types'

export type Scope = {
  project: string
  environment: string
  identity: Identity
  can: (permission: string) => boolean
  syncScope: (project: string, environment: string) => void
}
export const ScopeContext = createContext<Scope | null>(null)

// Browser credentials carry a broad session envelope; project roles determine
// which controls are available. Go independently authorizes every request.
export function canAccess(identity: Identity, project: string, permission: string) {
  if (identity.admin) return true
  if (identity.credential_type === 'browser') {
    const allowed: Record<string, string[]> = {
      admin: ['deployments:read', 'deployments:write', 'logs:read'],
      developer: ['deployments:read', 'deployments:write', 'logs:read'],
      viewer: ['deployments:read', 'logs:read'],
    }
    return Boolean(
      identity.project_roles?.some(
        (entry) => entry.project === project && allowed[entry.role]?.includes(permission),
      ),
    )
  }
  return identity.permissions.includes(permission)
}
export function useScope() {
  const context = useContext(ScopeContext)
  if (!context) throw new Error('Workspace context is unavailable. Reload the dashboard.')
  return context
}
