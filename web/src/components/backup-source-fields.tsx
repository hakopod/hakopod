import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Link } from '@tanstack/react-router'
import { Button } from './ui/button'
import {
  useBackupDestinations,
  useBackupTargets,
  sourceKey,
  sourceFromTarget,
  engineLabel,
  type BackupSource,
  type BackupTarget,
} from '../lib/backups'
import { ChoiceCards, type Choice } from './create-flow'
import { ResourceEmpty } from './resource-card'
import { ErrorState, Loading, Note } from './shared'

// Cards read well for a handful of choices; long discovered lists use the searchable select.
const cardLimit = 8

export const targetName = (item: BackupTarget) =>
  item.kind === 'management'
    ? 'Hakopod management database'
    : item.kind === 'managed_database'
      ? item.managed_database_name || item.application_name || item.managed_database_id || ''
      : item.kind === 'docker_import'
        ? item.external_name || 'Imported database'
        : `${item.application_name || item.application_id} / ${item.service}`

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
    return (
      <ErrorState
        error={targets.error || destinations.error}
        retry={() => void (targets.error ? targets.refetch() : destinations.refetch())}
      />
    )
  const items = targets.data?.items || []
  const saved = source && !items.some((item) => sourceKey(item) === sourceKey(source))
  const sourceChoices: Choice<string>[] = items.map((item) => ({
    value: sourceKey(item),
    title: targetName(item),
    description: engineLabel(item.engine) + (item.message ? ` · ${item.message}` : ''),
    disabled: !item.available,
    unavailable: `${engineLabel(item.engine)} · Unavailable${item.message ? `: ${item.message}` : ''}`,
  }))
  const pickSource = (value: string) => {
    const target = items.find((item) => sourceKey(item) === value)
    onSource(target ? sourceFromTarget(target) : null)
  }
  const destinationItems = destinations.data?.items || []
  return (
    <>
      <section className="create-section" aria-labelledby="backup-source-heading">
        <div className="create-section-heading">
          <h2 id="backup-source-heading">Database</h2>
          <span>Databases you can back up in this workspace</span>
        </div>
        {!items.length ? (
          <ResourceEmpty
            icon="database"
            title="No database can be backed up here"
            description="Deploy a supported database service or create a managed database first."
          />
        ) : items.length <= cardLimit ? (
          <ChoiceCards
            label="Database"
            compact
            value={source ? sourceKey(source) : ''}
            onChange={pickSource}
            choices={sourceChoices}
          />
        ) : (
          <label>
            Database
            <SelectField
              label="Database"
              value={source ? sourceKey(source) : ''}
              onValueChange={pickSource}
              required
              options={[
                { value: '', label: 'Choose an available database' },
                ...sourceChoices.map((choice) => ({
                  value: choice.value,
                  label: `${choice.title} · ${choice.disabled ? 'unavailable' : String(choice.description).split(' · ')[0]}`,
                  disabled: choice.disabled,
                })),
              ]}
            />
          </label>
        )}
        {saved && (
          <Note>
            The saved database is not currently discovered. Choose an available database, or keep
            the saved one and save again once it is running.
          </Note>
        )}
        {targets.data?.truncated && (
          <Note>Discovery stopped at 128 databases; some databases are not listed.</Note>
        )}
        {source?.kind === 'database' && (
          <label>
            Database name <span className="muted-text">(optional)</span>
            <Input
              value={source.database || ''}
              maxLength={128}
              onChange={(event) =>
                onSource({ ...source, database: event.target.value || undefined })
              }
              aria-describedby="backup-database-help"
            />
            <span id="backup-database-help" className="field-help">
              Leave empty to back up the service’s configured database. Cluster roles and external
              secret values are not part of the dump.
            </span>
          </label>
        )}
      </section>
      <section className="create-section" aria-labelledby="backup-destination-heading">
        <div className="create-section-heading">
          <h2 id="backup-destination-heading">Object storage</h2>
          <span>Where the encrypted backup is stored</span>
        </div>
        {!destinationItems.length ? (
          <ResourceEmpty
            icon="archive"
            title="No object storage yet"
            description="Backups are written to an S3-compatible bucket. Add one first."
            action={
              <Button asChild size="sm">
                <Link to="/backups/destinations/new">Add object storage</Link>
              </Button>
            }
          />
        ) : destinationItems.length <= cardLimit ? (
          <ChoiceCards
            label="Object storage"
            compact
            value={destination}
            onChange={onDestination}
            choices={destinationItems.map((item) => ({
              value: item.id,
              title: item.name,
              description: `${[item.bucket, item.prefix].filter(Boolean).join('/')} · ${item.region}`,
            }))}
          />
        ) : (
          <label>
            Object storage
            <SelectField
              label="Object storage"
              required
              value={destination}
              onValueChange={onDestination}
              options={[
                { value: '', label: 'Choose a saved destination' },
                ...destinationItems.map((item) => ({
                  value: item.id,
                  label: `${item.name} · ${item.bucket}`,
                })),
              ]}
            />
          </label>
        )}
      </section>
    </>
  )
}
