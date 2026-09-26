import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Link } from '@tanstack/react-router'
import {
  useBackupDestinations,
  useBackupTargets,
  sourceKey,
  sourceFromTarget,
  engineLabel,
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
        <SelectField
          label="Database source"
          value={source ? sourceKey(source) : ''}
          onValueChange={(value) => {
            const target = targets.data?.items.find((item) => sourceKey(item) === value)
            onSource(target ? sourceFromTarget(target) : null)
          }}
          required
          options={[
            {
              value: '',
              label: 'Choose an available database',
            },
            ...(source && !targets.data?.items.some((item) => sourceKey(item) === sourceKey(source))
              ? [{ value: sourceKey(source), label: 'Saved source · currently unavailable' }]
              : []),
            ...(targets.data?.items.map((item) => ({
              value: sourceKey(item),
              label:
                (item.kind === 'management'
                  ? 'Hakopod management database'
                  : `${item.application_name || item.application_id} / ${item.service} · ${engineLabel(item.engine)}`) +
                (!item.available ? ' · unavailable' : ''),
              disabled: !item.available,
            })) ?? []),
          ]}
        />
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
        <SelectField
          label="Object storage destination"
          required
          value={destination}
          onValueChange={(value) => onDestination(value)}
          options={[
            {
              value: '',
              label: 'Choose a saved destination',
            },
            ...(destinations.data?.items.map((item) => ({
              value: item.id,
              label: item.name + ' · ' + item.bucket,
            })) ?? []),
          ]}
        />
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
