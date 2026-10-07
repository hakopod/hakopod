// Grant identifiers match the canonical API. Execution remains explicitly selected.
export const workloadGrants = [
  ['deployments:read', 'Read applications and deployments'],
  ['deployments:write', 'Deploy and operate applications'],
  ['logs:read', 'Read logs and requests'],
  ['deployments:approve', 'Review workload requests'],
  ['pods:exec', 'Execute commands in service containers'],
  ['databases:query', 'Run read-only SQL queries'],
  ['databases:write-query', 'Run SQL queries that change data'],
  ['networks:write', 'Manage private networks'],
  ['git:manage', 'Manage Git sources and builds'],
  ['applications:manage', 'Delete applications'],
  ['agent:credentials', 'Retrieve authorized credentials'],
] as const
export const executionGrants = [
  ['pods:exec', 'Execute commands in service containers'],
  ['databases:query', 'Run read-only SQL queries'],
  ['databases:write-query', 'Run SQL queries that change data'],
  ['agent:credentials', 'Retrieve authorized credentials'],
] as const
export function toggleGrant(current: string[], grant: string, checked: boolean) {
  const next = new Set(current)
  if (checked) {
    next.add(grant)
    if (grant !== 'deployments:read') next.add('deployments:read')
    if (grant === 'databases:write-query') next.add('databases:query')
    if (grant === 'agent:credentials') next.add('deployments:write')
  } else {
    next.delete(grant)
    if (grant === 'deployments:read') return []
    if (grant === 'databases:query') next.delete('databases:write-query')
    if (grant === 'deployments:write') next.delete('agent:credentials')
  }
  return [...next]
}
