import { relative, timestamp } from '../lib/api'
import {
  engineLabel,
  sourceKey,
  sourceLabel,
  useBackupDestinations,
  useBackupTargets,
  type BackupSource,
} from '../lib/backups'

// Jobs, artifacts and schedules store IDs. Resolve them to the names people use,
// falling back to the recorded ID when the database or bucket is no longer listed.
export function useBackupNames() {
  const targets = useBackupTargets()
  const destinations = useBackupDestinations()
  const targetItems = targets.data?.items || []
  const destinationItems = destinations.data?.items || []
  return {
    source(source: BackupSource) {
      if (source.kind === 'management') return 'Hakopod management database'
      if (source.kind === 'docker_import') return source.external_name || 'Imported database'
      const target = targetItems.find((item) => sourceKey(item) === sourceKey(source))
      if (source.kind === 'managed_database')
        return target?.managed_database_name || target?.application_name || sourceLabel(source)
      return target
        ? `${target.application_name || source.application_id} / ${source.service}${source.database ? ` · ${source.database}` : ''}`
        : sourceLabel(source)
    },
    engine: (source: BackupSource) => engineLabel(source.engine),
    destination(id: string) {
      const item = destinationItems.find((destination) => destination.id === id)
      return item
        ? item.name
        : destinations.isPending
          ? 'Loading destination…'
          : `Removed destination ${id.slice(0, 8)}`
    },
    bucket(id: string) {
      const item = destinationItems.find((destination) => destination.id === id)
      return item ? [item.bucket, item.prefix].filter(Boolean).join('/') : ''
    },
    targets,
    destinations,
  }
}

// Elapsed time between two recorded instants; empty when either is unknown.
export function duration(start?: string, end?: string) {
  if (!start || !end) return ''
  const seconds = Math.max(0, Math.round((Date.parse(end) - Date.parse(start)) / 1000))
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m${seconds % 60 ? ` ${seconds % 60}s` : ''}`
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
}

// A future instant in words. A next run in the past is due, not "ago".
export function until(date: string) {
  const remaining = Date.parse(date) - Date.now()
  if (remaining <= 60000) return 'due now'
  if (remaining < 3600000) return `in ${Math.round(remaining / 60000)}m`
  if (remaining < 86400000) return `in ${Math.round(remaining / 3600000)}h`
  return `in ${Math.round(remaining / 86400000)}d`
}

export const everyHours = (hours: number) =>
  hours === 24
    ? 'daily'
    : hours === 168
      ? 'weekly'
      : hours % 24 === 0
        ? `every ${hours / 24} days`
        : hours === 1
          ? 'hourly'
          : `every ${hours} hours`

export function When({ date }: { date?: string }) {
  if (!date) return <span>Not recorded</span>
  return (
    <time dateTime={date} title={timestamp(date)}>
      {relative(date)}
    </time>
  )
}
