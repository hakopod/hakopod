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
export function useScope() {
  const context = useContext(ScopeContext)
  if (!context) throw new Error('Workspace context is unavailable. Reload the dashboard.')
  return context
}
