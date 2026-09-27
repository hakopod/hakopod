import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from './client'
import type { components } from './api.generated'

export type ManagedDatabase = components['schemas']['ManagedDatabase']
export type DatabaseSpec = components['schemas']['ManagedDatabaseSpec']
export type DatabaseResizeReview = components['schemas']['DatabaseResizePlan']
export const databaseSearch = (search: Record<string, unknown>) => ({
  project: typeof search.project === 'string' ? search.project : '',
  environment: typeof search.environment === 'string' ? search.environment : '',
})
export function useDatabase(id?: string) {
  return useQuery({
    queryKey: ['managed-database', id],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/databases/{id}', { signal, params: { path: { id: id! } } })),
    enabled: Boolean(id),
    refetchInterval: 5000,
    gcTime: 0,
  })
}
export function useDatabases(project: string, environment: string, enabled = true) {
  return useQuery({
    queryKey: ['managed-databases', project, environment],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/databases', { signal, params: { query: { project, environment } } })),
    enabled: enabled && Boolean(project && environment),
    refetchInterval: 5000,
    gcTime: 0,
  })
}
export const databaseSummary = (spec: DatabaseSpec) =>
  `${spec.engine === 'postgresql' ? 'PostgreSQL' : 'Redis'} ${spec.version} · ${spec.mode === 'standalone' ? 'Standalone' : spec.engine === 'redis' ? `${spec.shards} shards · ${spec.replicas} replicas per shard` : `${spec.replicas} replicas`}`

export function databaseHealth(d: ManagedDatabase, now = Date.now()) {
  if (
    !d.observation.status ||
    !d.observation.revision ||
    d.observation.observed_at?.startsWith('0001-')
  )
    return 'Not observed'
  const stamp = Date.parse(d.observation.observed_at)
  if (!Number.isFinite(stamp)) return 'Not observed'
  if (d.observation.revision !== d.revision || stamp > now || now - stamp > 30_000)
    return 'Observation stale'
  return d.observation.status || 'Unknown'
}
