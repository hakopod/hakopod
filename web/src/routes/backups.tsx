import { Input } from '../components/ui/input'
import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import type { components } from '../lib/api.generated'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { useActiveSection } from '../lib/use-active-section'
import {
  byteSize,
  engineManaged,
  sourceLabel,
  useBackupDestinations,
  useBackupSchedules,
  type BackupDestination,
  type BackupSchedule,
} from '../lib/backups'
import { Button } from '../components/ui/button'
import { RowActions } from '../components/ui/row-actions'
import { Dialog } from '../components/ui/dialog'
import { Icon } from '../components/icons'
import {
  HeadingHelp,
  Copy,
  Empty,
  ErrorState,
  Loading,
  Note,
  PageHeader,
  Status,
} from '../components/shared'
import {
  ResourceEmpty,
  ResourceError,
  ResourceRow,
  ResourceRows,
  StaleNotice,
} from '../components/resource-card'

export const Route = createFileRoute('/backups')({
  validateSearch: (search: Record<string, unknown>): { tab?: string } => ({
    tab: ['jobs', 'artifacts', 'destinations', 'schedules'].includes(String(search.tab))
      ? String(search.tab)
      : undefined,
  }),
  component: BackupsRoute,
})
function BackupsRoute() {
  const scope = useScope()
  const path = useLocation().pathname
  if (!scope.identity.admin && !scope.identity.can_manage_backups)
    return (
      <>
        <PageHeader title="Backups & restores" />
        <Empty
          icon="lock"
          title="Write access required"
          description="Choose a workspace where you can manage applications to configure database backups."
        />
      </>
    )
  return path === '/backups' ? <Backups /> : <Outlet />
}
function Backups() {
  const { tab = 'jobs' } = Route.useSearch()
  const navigationRoot = useActiveSection(tab, '.tab-list')
  const navigate = Route.useNavigate()
  return (
    <>
      <PageHeader
        eyebrow="DATA PROTECTION"
        title="Backups & restores"
        description="Encrypted database backups, object storage destinations, and reviewed restores."
        action={
          tab === 'destinations' ? (
            <Button asChild variant="primary">
              <Link to="/backups/destinations/new">
                <Icon name="plus" size={15} />
                Add destination
              </Link>
            </Button>
          ) : tab === 'schedules' ? (
            <Button asChild variant="primary">
              <Link to="/backups/schedules/new">
                <Icon name="plus" size={15} />
                Create schedule
              </Link>
            </Button>
          ) : (
            <div className="flex flex-wrap items-center gap-2">
              {tab !== 'artifacts' && (
                <Button asChild>
                  <Link to="/backups" search={{ tab: 'artifacts' }}>
                    Restore backup
                  </Link>
                </Button>
              )}
              <Button asChild variant="primary">
                <Link to="/backups/new">
                  <Icon name="plus" size={15} />
                  Run backup
                </Link>
              </Button>
            </div>
          )
        }
      />
      <nav ref={navigationRoot} className="tab-list" aria-label="Backup sections">
        {[
          ['jobs', 'Job history'],
          ['artifacts', 'Stored backups'],
          ['destinations', 'Object storage'],
          ['schedules', 'Schedules'],
        ].map(([value, label]) => (
          <button
            key={value}
            className={`tab-trigger ${tab === value ? 'selected' : ''}`}
            data-state={tab === value ? 'active' : 'inactive'}
            aria-current={tab === value ? 'page' : undefined}
            onClick={() => void navigate({ search: { tab: value } })}
          >
            {label}
          </button>
        ))}
      </nav>
      <div className="tab-content">
        {tab === 'destinations' ? (
          <Destinations />
        ) : tab === 'schedules' ? (
          <Schedules />
        ) : (
          <BackupHistory key={tab} artifacts={tab === 'artifacts'} />
        )}
      </div>
      <p className="backup-resource-note">
        Need more control over scheduled backups, restores and migrations?{' '}
        <a href="https://data.synehq.com" target="_blank" rel="noopener noreferrer">
          Explore Syne Data <span className="sr-only">(opens in a new tab)</span>
        </a>
        .
      </p>
    </>
  )
}
function BackupHistory({ artifacts }: { artifacts: boolean }) {
  const [page, setPage] = useState({ cursor: '', previous: [] as string[] })
  const [remove, setRemove] = useState<components['schemas']['BackupArtifact'] | null>(null)
  const jobs = useQuery({
    queryKey: ['backups', page.cursor],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/backups', { signal, params: { query: { cursor: page.cursor || undefined } } }),
      ),
    enabled: !artifacts,
    gcTime: 0,
  })
  const stored = useQuery({
    queryKey: ['backup-artifacts', page.cursor],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/backup-artifacts', {
          signal,
          params: { query: { cursor: page.cursor || undefined } },
        }),
      ),
    enabled: artifacts,
    gcTime: 0,
  })
  const active = artifacts ? stored : jobs
  const next = active.data?.next_cursor
  const removal = stored.data?.items.find((item) => item.id === remove?.id)
  return (
    <>
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>{artifacts ? 'Stored database backups' : 'Backup and restore jobs'}</h2>
            <HeadingHelp title={artifacts ? 'Stored database backups' : 'Backup and restore jobs'}>
              Up to 100 records per page.
            </HeadingHelp>
          </div>
        </div>
        <Button size="sm" onClick={() => void active.refetch()}>
          <Icon name="refresh" size={14} />
          Refresh
        </Button>
      </div>
      {active.isPending ? (
        <Loading />
      ) : active.error && !active.data ? (
        <ResourceError
          error={active.error}
          retry={() => void active.refetch()}
          subject={artifacts ? 'stored backups' : 'backup jobs'}
        />
      ) : !active.data?.items.length ? (
        <ResourceEmpty
          icon="archive"
          title={artifacts ? 'No stored backups' : 'No backup jobs yet'}
          description={
            artifacts
              ? 'Completed backups appear here, ready to restore into a new database.'
              : 'Run a backup after saving an object storage destination.'
          }
        />
      ) : (
        <>
          {active.error && (
            <StaleNotice updatedAt={active.dataUpdatedAt} retry={() => void active.refetch()} />
          )}
          <ResourceRows label={artifacts ? 'Stored database backups' : 'Backup and restore jobs'}>
            {artifacts
              ? stored.data?.items.map((item) => (
                  <ResourceRow
                    key={item.id}
                    identity={
                      <>
                        <code title={item.object_key}>
                          {item.object_key.length > 48
                            ? `…${item.object_key.slice(-47)}`
                            : item.object_key}
                        </code>
                        <HeadingHelp title="Recovery scope">{item.scope}</HeadingHelp>
                      </>
                    }
                    detail={
                      <>
                        <span>{sourceLabel(item.source)}</span>
                        {engineManaged(item) && (
                          <span>Object prefix · size reported by the engine</span>
                        )}
                        {item.deletion_pending && <span>Deletion pending</span>}
                      </>
                    }
                    status={<span className="resource-row-count">{byteSize(item.bytes)}</span>}
                    meta={timestamp(item.created_at)}
                    actions={
                      <>
                        {item.deletion_pending ? (
                          <Button size="sm" disabled>
                            Restore
                          </Button>
                        ) : (
                          <Button asChild size="sm">
                            <Link
                              to="/backups/artifacts/$artifactId/restore"
                              params={{ artifactId: item.id }}
                            >
                              Restore
                            </Link>
                          </Button>
                        )}
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={item.deletion_pending}
                          onClick={() => setRemove(item)}
                        >
                          <Icon name="trash" />
                          Delete
                        </Button>
                      </>
                    }
                  />
                ))
              : jobs.data?.items.map((item) => (
                  <ResourceRow
                    key={item.id}
                    identity={
                      <Link
                        className="resource-row-link"
                        to="/backups/$jobId"
                        params={{ jobId: item.id }}
                      >
                        {item.kind === 'restore' ? 'Restore database' : 'Database backup'}
                      </Link>
                    }
                    detail={
                      <>
                        <code>{item.id.slice(0, 12)}</code>
                        <span>{sourceLabel(item.source)}</span>
                      </>
                    }
                    status={<Status value={item.status} small />}
                    meta={timestamp(item.created_at)}
                  />
                ))}
          </ResourceRows>
        </>
      )}
      {(page.previous.length > 0 || next) && (
        <div className="table-pagination">
          <Button
            size="sm"
            disabled={!page.previous.length}
            onClick={() =>
              setPage({ cursor: page.previous.at(-1) || '', previous: page.previous.slice(0, -1) })
            }
          >
            Previous
          </Button>
          <span>Page {page.previous.length + 1}</span>
          <Button
            size="sm"
            disabled={!next || page.previous.length >= 20}
            onClick={() =>
              next && setPage({ cursor: next, previous: [...page.previous, page.cursor] })
            }
          >
            Next
          </Button>
        </div>
      )}
      {removal && <RemoveArtifact artifact={removal} onClose={() => setRemove(null)} />}
    </>
  )
}
function RemoveArtifact({
  artifact,
  onClose,
}: {
  artifact: components['schemas']['BackupArtifact']
  onClose: () => void
}) {
  const cache = useQueryClient()
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose()
      }}
      title="Delete stored backup?"
      description={
        engineManaged(artifact)
          ? 'This permanently deletes every object the database engine wrote under this prefix in the configured bucket.'
          : 'This permanently deletes the encrypted object from the configured bucket.'
      }
    >
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy || artifact.deletion_pending || confirmation !== artifact.id) return
          setBusy(true)
          setError('')
          try {
            await unwrap(
              client.DELETE('/backup-artifacts/{id}', {
                params: { path: { id: artifact.id } },
                body: { confirmation },
              }),
            )
            void cache.invalidateQueries({ queryKey: ['backup-artifacts'] })
            onClose()
          } catch (err) {
            setError(message(err))
            void cache.invalidateQueries({ queryKey: ['backup-artifacts'] })
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="dialog-body auth-form">
          <dl className="service-definition-list">
            <div>
              <dt>Source</dt>
              <dd>{sourceLabel(artifact.source)}</dd>
            </div>
            <div>
              <dt>{engineManaged(artifact) ? 'Object prefix' : 'Object'}</dt>
              <dd className="break-text">
                <code>{artifact.object_key}</code>
              </dd>
            </div>
            <div>
              <dt>Created</dt>
              <dd>{timestamp(artifact.created_at)}</dd>
            </div>
          </dl>
          <Note>An active restore blocks deletion. This action cannot be undone.</Note>
          {artifact.deletion_pending && (
            <Note>
              Deletion pending. Hakopod will retry removing this object if the storage provider is
              temporarily unavailable.
            </Note>
          )}
          <label>
            Type the backup ID to confirm
            <code className="field-help break-text">{artifact.id}</code>
            <Input
              value={confirmation}
              disabled={artifact.deletion_pending}
              onChange={(event) => setConfirmation(event.target.value)}
              autoComplete="off"
              autoCapitalize="none"
              autoCorrect="off"
              required
            />
          </label>
          {error && <ErrorState error={error} />}
        </div>
        <div className="dialog-footer">
          <Button type="button" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            variant="danger"
            disabled={busy || artifact.deletion_pending || confirmation !== artifact.id}
          >
            {busy ? 'Deleting…' : 'Delete stored backup'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
function Destinations() {
  const destinations = useBackupDestinations()
  const navigate = useNavigate()
  const [remove, setRemove] = useState<BackupDestination | null>(null)
  const [testing, setTesting] = useState('')
  const [result, setResult] = useState('')
  const [error, setError] = useState('')
  async function test(item: BackupDestination) {
    if (testing) return
    setTesting(item.id)
    setError('')
    setResult('')
    try {
      const value = await unwrap(
        client.POST('/backup-destinations/{id}/test', {
          params: { path: { id: item.id } },
          body: {},
        }),
      )
      setResult(`${item.name}: ${value.message}`)
    } catch (err) {
      setError(message(err))
    } finally {
      setTesting('')
    }
  }
  return (
    <>
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>Object storage</h2>
            <HeadingHelp title="Object storage">
              Connect and test the buckets used by backups. Credentials stay on the server.
            </HeadingHelp>
          </div>
        </div>
      </div>
      {destinations.isPending ? (
        <Loading />
      ) : destinations.error && !destinations.data ? (
        <ResourceError
          error={destinations.error}
          retry={() => void destinations.refetch()}
          subject="backup destinations"
        />
      ) : !destinations.data?.items.length ? (
        <ResourceEmpty
          icon="archive"
          title="No destinations configured"
          description="Backups need an S3-compatible bucket to store encrypted exports."
          action={
            <Button asChild>
              <Link to="/backups/destinations/new">Add destination</Link>
            </Button>
          }
        />
      ) : (
        <ResourceRows label="Object storage destinations">
          {destinations.data.items.map((item) => (
            <ResourceRow
              key={item.id}
              identity={
                <Link
                  className="resource-row-link"
                  to="/backups/destinations/$destinationId/edit"
                  params={{ destinationId: item.id }}
                >
                  {item.name}
                </Link>
              }
              detail={
                <>
                  <span>{item.endpoint}</span>
                  <span>
                    {item.bucket} / {item.prefix || 'bucket root'}
                  </span>
                  <span className="copy-id">
                    <code>{item.id.slice(0, 12)}</code>
                    <Copy value={item.id} label={`Copy destination ID ${item.id}`} />
                  </span>
                  <span className="copy-id">
                    <code title={item.encryption_recipient}>
                      {item.encryption_recipient.slice(0, 18)}…
                    </code>
                    <Copy
                      value={item.encryption_recipient}
                      label={`Copy encryption recipient for ${item.name}`}
                    />
                  </span>
                </>
              }
              meta={item.region}
              actions={
                <RowActions
                  label={item.name}
                  actions={[
                    {
                      label: 'Edit destination',
                      run: () =>
                        void navigate({
                          to: '/backups/destinations/$destinationId/edit',
                          params: { destinationId: item.id },
                        }),
                    },
                    {
                      label: testing === item.id ? 'Testing connection…' : 'Test connection',
                      run: () => void test(item),
                      disabled: Boolean(testing),
                    },
                    {
                      label: 'Remove destination',
                      run: () => setRemove(item),
                      destructive: true,
                    },
                  ]}
                />
              }
            />
          ))}
        </ResourceRows>
      )}
      {result && (
        <div role="status">
          <Note>{result}</Note>
        </div>
      )}
      {error && <ErrorState error={error} />}
      {remove && (
        <RemoveBackupConfig
          kind="destination"
          id={remove.id}
          name={remove.name}
          revision={remove.revision}
          onClose={() => setRemove(null)}
        />
      )}
    </>
  )
}
function Schedules() {
  const schedules = useBackupSchedules()
  const navigate = useNavigate()
  const [remove, setRemove] = useState<BackupSchedule | null>(null)
  return (
    <>
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>Backup schedules</h2>
            <HeadingHelp title="Backup schedules">
              Review the next run, source, and retained backup count.
            </HeadingHelp>
          </div>
        </div>
      </div>
      {schedules.isPending ? (
        <Loading />
      ) : schedules.error && !schedules.data ? (
        <ResourceError
          error={schedules.error}
          retry={() => void schedules.refetch()}
          subject="backup schedules"
        />
      ) : !schedules.data?.items.length ? (
        <ResourceEmpty
          icon="clock"
          title="No schedules yet"
          description="A schedule backs up one database at a fixed interval and keeps a set number."
          action={
            <Button asChild>
              <Link to="/backups/schedules/new">Create schedule</Link>
            </Button>
          }
        />
      ) : (
        <ResourceRows label="Backup schedules">
          {schedules.data.items.map((item) => (
            <ResourceRow
              key={item.id}
              identity={
                <Link
                  className="resource-row-link"
                  to="/backups/schedules/$scheduleId/edit"
                  params={{ scheduleId: item.id }}
                >
                  {item.name}
                </Link>
              }
              detail={
                <>
                  <code>{item.id.slice(0, 12)}</code>
                  <span>{sourceLabel(item.source)}</span>
                  <span>
                    Every {item.interval_hours} hours · keeps {item.retention_count} backups
                  </span>
                </>
              }
              status={<Status value={item.enabled ? 'active' : 'paused'} small />}
              meta={item.enabled ? `Next ${timestamp(item.next_run_at)}` : 'Paused'}
              actions={
                <RowActions
                  label={item.name}
                  actions={[
                    {
                      label: 'Edit schedule',
                      run: () =>
                        void navigate({
                          to: '/backups/schedules/$scheduleId/edit',
                          params: { scheduleId: item.id },
                        }),
                    },
                    { label: 'Remove schedule', run: () => setRemove(item), destructive: true },
                  ]}
                />
              }
            />
          ))}
        </ResourceRows>
      )}
      {remove && (
        <RemoveBackupConfig
          kind="schedule"
          id={remove.id}
          name={remove.name}
          revision={remove.revision}
          onClose={() => setRemove(null)}
        />
      )}
    </>
  )
}
function RemoveBackupConfig({
  kind,
  id,
  name,
  revision,
  onClose,
}: {
  kind: 'destination' | 'schedule'
  id: string
  name: string
  revision: number
  onClose: () => void
}) {
  const cache = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [confirmation, setConfirmation] = useState('')
  const [error, setError] = useState('')
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose()
      }}
      title={`Remove ${name}?`}
      description={
        kind === 'destination'
          ? 'The server checks whether jobs or schedules still need this destination.'
          : 'Future automatic jobs for this schedule will stop.'
      }
    >
      <div className="dialog-body">
        <Note>Stored backup objects are not deleted by this action.</Note>
        <label>
          Type {name} to remove this {kind}
          <Input
            value={confirmation}
            onChange={(event) => setConfirmation(event.target.value)}
            autoComplete="off"
          />
        </label>
        {error && <ErrorState error={error} />}
      </div>
      <div className="dialog-footer">
        <Button disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        <Button
          variant="danger"
          disabled={busy || confirmation !== name}
          onClick={async () => {
            if (busy || confirmation !== name) return
            setBusy(true)
            setError('')
            try {
              if (kind === 'destination')
                await unwrap(
                  client.DELETE('/backup-destinations/{id}', {
                    params: { path: { id } },
                    body: { expected_revision: revision },
                  }),
                )
              else
                await unwrap(
                  client.DELETE('/backup-schedules/{id}', {
                    params: { path: { id } },
                    body: { expected_revision: revision },
                  }),
                )
              void cache.invalidateQueries({
                queryKey: [kind === 'destination' ? 'backup-destinations' : 'backup-schedules'],
              })
              onClose()
            } catch (err) {
              setError(message(err))
            } finally {
              setBusy(false)
            }
          }}
        >
          {busy ? 'Removing…' : 'Remove'}
        </Button>
      </div>
    </Dialog>
  )
}
