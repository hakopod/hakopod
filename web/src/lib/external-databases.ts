import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from './client'
import type { components } from './api.generated'

export type ExternalDatabase = components['schemas']['ExternalDatabase']
export type ExternalDatabaseSpec = components['schemas']['ExternalDatabaseSpec']
export type ExternalDatabaseCredentials = components['schemas']['ExternalDatabaseCredentials']
export type ExternalDatabaseConnectionPlan = components['schemas']['ExternalDatabaseConnectionPlan']

export function useExternalDatabases(project: string, environment: string, enabled = true) {
  return useQuery({
    queryKey: ['external-databases', project, environment],
    queryFn: ({ signal }) => unwrap(client.GET('/external-databases', { signal, params: { query: { project, environment } } })),
    enabled: enabled && Boolean(project && environment), refetchInterval: 10000, gcTime: 0,
  })
}
export function useExternalDatabase(id: string) {
  return useQuery({
    queryKey: ['external-database', id],
    queryFn: ({ signal }) => unwrap(client.GET('/external-databases/{id}', { signal, params: { path: { id } } })),
    enabled: Boolean(id), refetchInterval: 10000, gcTime: 0,
  })
}
export function useExternalDatabaseConnections(id: string) {
  return useQuery({
    queryKey: ['external-database-connections', id],
    queryFn: ({ signal }) => unwrap(client.GET('/external-databases/{id}/connections', { signal, params: { path: { id } } })),
    enabled: Boolean(id), refetchInterval: 10000, gcTime: 0,
  })
}
export function externalDatabaseHealth(database: ExternalDatabase, now = Date.now()) {
  const observation = database.observation
  if (!observation.revision || observation.observed_at?.startsWith('0001-')) return 'Not observed'
  const observedAt = Date.parse(observation.observed_at)
  if (!Number.isFinite(observedAt)) return 'Not observed'
  if (observation.revision !== database.revision || observedAt > now + 5000 || now - observedAt > 120000) return 'Observation stale'
  if (database.status !== 'ready') return database.status
  if (observation.status === 'ready') return observation.tls_verified && observation.query_verified ? 'ready' : 'unreachable'
  return observation.status || 'unreachable'
}
export function externalDatabaseIssue(spec: ExternalDatabaseSpec, credentials?: ExternalDatabaseCredentials) {
  if (!/^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$/.test(spec.name)) return 'Use 1–40 lowercase letters, numbers or hyphens. Start with a letter and end with a letter or number.'
  if (!['mysql', 'postgresql'].includes(spec.engine)) return 'Choose MySQL or PostgreSQL.'
  if (spec.host.length > 253 || !/^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+psdb\.(?:cloud|io)$/.test(spec.host)) return 'Enter the provider-issued psdb.cloud or psdb.io hostname without a protocol or port.'
  if (spec.engine === 'mysql' ? spec.port !== 3306 : ![5432, 6432].includes(spec.port)) return 'Use port 3306 for MySQL, or 5432 or 6432 for PostgreSQL.'
  if (!spec.database || spec.database.length > 64 || /[/\\?#\x00\r\n]/.test(spec.database)) return 'Enter a database name of up to 64 characters without URL separators.'
  if (credentials && (!credentials.username || credentials.username.length > 256 || !credentials.password || credentials.password.length > 4096 || /[\x00\r\n]/.test(credentials.username + credentials.password))) return 'Enter the provider-issued username and password without line breaks.'
}
