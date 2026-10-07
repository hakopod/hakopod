import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from './client'
import type { components } from './api.generated'

type GeneratedDatabaseSpec = components['schemas']['ManagedDatabaseSpec']
export type DatabaseEngine = GeneratedDatabaseSpec['engine'] | 'duckdb'
export type DatabaseSpec = Omit<GeneratedDatabaseSpec, 'engine'> & { engine: DatabaseEngine }
export type ManagedDatabase = Omit<components['schemas']['ManagedDatabase'], 'spec'> & { spec: DatabaseSpec }
export const apiDatabaseSpec = (spec: DatabaseSpec) => spec as unknown as GeneratedDatabaseSpec
export type DatabaseResizeReview = components['schemas']['DatabaseResizePlan']
export type DatabaseConnectionReference = components['schemas']['DatabaseConnectionReference']
export type DatabaseOperation = components['schemas']['DatabaseOperation']
export type DatabasePublicEndpoint = components['schemas']['DatabasePublicEndpoint']
export type DatabasePublicEndpointPlan = components['schemas']['DatabasePublicEndpointPlan']
export type DatabasePublicEndpointOperation = components['schemas']['DatabasePublicEndpointOperation']
export type DatabasePlacementNode = components['schemas']['DatabasePlacementNode']
export type DatabasePublicEndpointRoute = components['schemas']['DatabasePublicEndpointRoute']
export type DatabasePublicEndpointCapabilities = components['schemas']['DatabasePublicEndpointCapabilities']
export function useDatabaseQueryCapabilities(id: string, enabled = true) {
  return useQuery({
    queryKey: ['database-query-capabilities', id],
    queryFn: ({ signal }) => unwrap(client.GET('/databases/{id}/query-capabilities', { signal, params: { path: { id } } })),
    enabled, refetchInterval: 30000, gcTime: 0,
  })
}
export function useDatabasePlacementNodes(project: string, environment: string, enabled = true) {
  return useQuery({
    queryKey: ['database-placement-nodes', project, environment],
    queryFn: ({ signal }) => unwrap(client.GET('/database-placement/nodes', { signal, params: { query: { project, environment } } })),
    enabled: enabled && Boolean(project && environment), refetchInterval: 30000, gcTime: 0,
  })
}
export function useDatabaseOperations(id: string, enabled = true) {
  return useQuery({
    queryKey: ['database-operations', id],
    queryFn: ({ signal }) => unwrap(client.GET('/databases/{id}/operations', { signal, params: { path: { id } } })),
    enabled, refetchInterval: 5000, gcTime: 0,
  })
}
export function useDatabasePublicEndpoints(id: string, enabled = true) {
  return useQuery({
    queryKey: ['database-public-endpoints', id],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/databases/{id}/public-endpoints', { signal, params: { path: { id } } })),
    enabled,
    refetchInterval: 5000,
    gcTime: 0,
  })
}
export function useDatabasePublicEndpointCapabilities(id: string, enabled = true) {
  return useQuery<DatabasePublicEndpointCapabilities>({
    queryKey: ['database-public-endpoint-capabilities', id],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/databases/{id}/public-endpoint-capabilities', {
          signal,
          params: { path: { id } },
        }),
      ),
    enabled,
    refetchInterval: 5000,
    gcTime: 0,
  })
}
export function oracleSwitchoverNeedsFreshReview(d: ManagedDatabase, latest?: DatabaseOperation) {
  return d.status === 'failed' && latest?.database_id === d.id && latest.revision === d.revision &&
    latest.kind === 'switchover' && latest.status === 'failed' && latest.phase === 'review'
}
export function useDatabaseConnections(id: string) {
  return useQuery({
    queryKey: ['database-connections', id],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/databases/{id}/connections', { signal, params: { path: { id } } })),
    refetchInterval: 15000,
    gcTime: 0,
  })
}
export const databaseSearch = (search: Record<string, unknown>) => ({
  project: typeof search.project === 'string' ? search.project : '',
  environment: typeof search.environment === 'string' ? search.environment : '',
})
export function useDatabase(id?: string) {
  return useQuery<ManagedDatabase>({
    queryKey: ['managed-database', id],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/databases/{id}', { signal, params: { path: { id: id! } } })) as unknown as Promise<ManagedDatabase>,
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
  `${({ postgresql: 'PostgreSQL', mysql: 'MySQL', redis: 'Redis', mongodb: 'MongoDB', clickhouse: 'ClickHouse', oracle: 'Oracle Database', vitess: 'Vitess', duckdb: 'DuckDB (MyDuck)' })[spec.engine]} ${spec.version} · ${spec.mode === 'standalone' ? 'Standalone' : ['redis', 'clickhouse', 'vitess'].includes(spec.engine) ? `${spec.shards} shards · ${spec.replicas} replicas per shard` : `${spec.replicas} ${spec.replicas === 1 ? 'replica' : 'replicas'}`}`

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
