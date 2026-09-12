import { Input } from './ui/input'
import { Select } from './ui/select'
import { Link } from '@tanstack/react-router'
import {
  useBackupDestinations,
  useBackupTargets,
  sourceKey,
  sourceFromTarget,
  type BackupSource,
} from '../lib/backups'
import { ErrorState, Loading, Note } from './shared'

export function BackupSourceFields({
  source,
  onSource,
  destination,
  onDestination,
}: {
  source: BackupSource | null
  onSource: (value: BackupSource | null) => void
  destination: string
  onDestination: (value: string) => void
}) {
  const targets = useBackupTargets()
  const destinations = useBackupDestinations()
  if (targets.isPending || destinations.isPending) return <Loading rows={2} />
  if (targets.error || destinations.error)
    return <ErrorState error={targets.error || destinations.error} />
  return (
    <>
      <label>
        Database source
        <Select
          value={source ? sourceKey(source) : ''}
          onChange={(event) => {
            const target = targets.data?.items.find(
              (item) => sourceKey(item) === event.target.value,
            )
            onSource(target ? sourceFromTarget(target) : null)
          }}
          required
        >
          <option value="">Choose an available database</option>
          {source && !targets.data?.items.some((item) => sourceKey(item) === sourceKey(source)) && (
            <option value={sourceKey(source)}>Saved source · currently unavailable</option>
          )}
          {targets.data?.items.map((item) => (
            <option key={sourceKey(item)} value={sourceKey(item)} disabled={!item.available}>
              {item.kind === 'management'
                ? 'Hakopod management database'
                : `${item.application_name || item.application_id} / ${item.service} · ${item.engine}`}
              {!item.available ? ' · unavailable' : ''}
            </option>
          ))}
        </Select>
      </label>
      {targets.data?.truncated && <Note>The discovery list reached its 128-target limit.</Note>}
      {source?.kind === 'database' && (
        <label>
          Database name
          <Input
            value={source.database || ''}
            maxLength={128}
            placeholder="Use the service’s configured database"
            onChange={(event) => onSource({ ...source, database: event.target.value || undefined })}
          />
          <span className="field-help">
            Backups cover this database. Cluster roles and external secret values are outside the
            database dump.
          </span>
        </label>
      )}
      <label>
        Object storage destination
        <Select
          required
          value={destination}
          onChange={(event) => onDestination(event.target.value)}
        >
          <option value="">Choose a saved destination</option>
          {destinations.data?.items.map((item) => (
            <option key={item.id} value={item.id}>
              {item.name} · {item.bucket}
            </option>
          ))}
        </Select>
      </label>
      {!destinations.data?.items.length && (
        <Note>
          Add an <Link to="/backups/destinations/new">object storage destination</Link> before
          starting a backup.
        </Note>
      )}
    </>
  )
}
