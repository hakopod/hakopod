import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from './client'
import type { components } from './api.generated'
export type BackupSource = components['schemas']['BackupSource']
export type BackupTarget = components['schemas']['BackupTarget']
export type BackupDestination = components['schemas']['BackupDestination']
export type BackupSchedule = components['schemas']['BackupSchedule']
export const sourceKey = (source: BackupSource) =>
  `${source.kind}/${source.application_id || ''}/${source.service || ''}/${source.engine}`
export const engineLabels: Record<string, string> = {
  postgresql: 'PostgreSQL',
  mysql: 'MySQL',
  clickhouse: 'ClickHouse',
}
export const engineLabel = (engine: string) => engineLabels[engine] || engine
// The server writes this format prefix only for a backup the database engine
// performed itself. Such an artifact has no server-computed digest, its byte
// count is the engine's own, and its object key is a prefix holding a tree of
// objects rather than one object.
export const engineManaged = (artifact: { format: string }) => artifact.format.startsWith('engine:')

// Engines that perform their own backup. Mirrors EngineManaged in
// internal/backup/service.go. A running job carries its engine before any
// artifact exists, so the artifact format cannot answer this mid-run.
export const engineManagedEngines = ['clickhouse']

export const engineManagedEngine = (engine?: string) =>
  !!engine && engineManagedEngines.includes(engine)
export const sourceLabel = (source: BackupSource) =>
  source.kind === 'management'
    ? 'Hakopod management database'
    : `${source.application_id || ''} / ${source.service || ''} · ${engineLabel(source.engine)}${source.database ? ` / ${source.database}` : ''}`
export const sourceFromTarget = (target: BackupTarget): BackupSource => ({
  kind: target.kind,
  engine: target.engine,
  application_id: target.application_id,
  service: target.service,
  database: target.database,
})
// A missing or zero byte count means nothing has been reported yet, which is not
// the same as a measured zero, so it must not read as a size.
export const byteSize = (bytes?: number) =>
  !bytes
    ? 'Not reported'
    : bytes >= 1024 ** 3
      ? `${(bytes / 1024 ** 3).toFixed(1)} GiB`
      : bytes >= 1024 ** 2
        ? `${(bytes / 1024 ** 2).toFixed(1)} MiB`
        : `${(bytes / 1024).toFixed(1)} KiB`
export function useBackupDestinations() {
  return useQuery({
    queryKey: ['backup-destinations'],
    queryFn: ({ signal }) => unwrap(client.GET('/backup-destinations', { signal })),
    gcTime: 0,
  })
}
export function useBackupTargets() {
  return useQuery({
    queryKey: ['backup-targets'],
    queryFn: ({ signal }) => unwrap(client.GET('/backup-targets', { signal })),
    gcTime: 0,
  })
}
export function useBackupSchedules() {
  return useQuery({
    queryKey: ['backup-schedules'],
    queryFn: ({ signal }) => unwrap(client.GET('/backup-schedules', { signal })),
    gcTime: 0,
  })
}
