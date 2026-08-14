import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import type { components } from '../lib/api.generated'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import {
  byteSize,
  sourceLabel,
  useBackupDestinations,
  useBackupSchedules,
  type BackupDestination,
  type BackupSchedule,
} from '../lib/backups'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Icon } from '../components/icons'
import { Empty, ErrorState, Loading, Note, PageHeader, Status } from '../components/shared'

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
  if (!scope.identity.admin)
    return (
      <Empty
        icon="lock"
        title="Administrator access required"
        description="Database backup and restore operations are managed by installation administrators."
      />
    )
  return path === '/backups' ? <Backups /> : <Outlet />
}
function Backups() {
  const { tab = 'jobs' } = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <>
      <PageHeader
        eyebrow="OPERATOR / DATA PROTECTION"
        title="Backups"
        description="Encrypted database backups, object storage destinations, and reviewed restores."
        action={
          <Link className="button button-primary" to="/backups/new">
            <Icon name="plus" size={15} />
            Run backup
          </Link>
        }
      />
      <nav className="tab-list" aria-label="Backup sections">
        {[
          ['jobs', 'Job history'],
          ['artifacts', 'Stored backups'],
          ['destinations', 'Object storage'],
          ['schedules', 'Schedules'],
        ].map(([value, label]) => (
          <button
            key={value}
            className={`tab-trigger ${tab === value ? 'selected' : ''}`}
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
          <h2>{artifacts ? 'Stored database backups' : 'Backup and restore jobs'}</h2>
          <p>Up to 100 records per page.</p>
        </div>
        <Button size="sm" onClick={() => void active.refetch()}>
          <Icon name="refresh" size={14} />
          Refresh
        </Button>
      </div>
      {active.isPending ? (
        <Loading />
      ) : active.error ? (
        <ErrorState error={active.error} />
      ) : !active.data?.items.length ? (
        <Empty
          icon="archive"
          title={artifacts ? 'No stored backups' : 'No backup jobs yet'}
          description="Run a backup after saving an object storage destination."
        />
      ) : (
        <div className="table-container">
          <table>
            <thead>
              <tr>
                <th>{artifacts ? 'Stored at' : 'Job'}</th>
                <th>Source</th>
                <th>{artifacts ? 'Size' : 'Status'}</th>
                <th>Created</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {artifacts
                ? stored.data?.items.map((item) => (
                    <tr key={item.id}>
                      <td>
                        <code title={item.object_key}>
                          {item.object_key.length > 48
                            ? `…${item.object_key.slice(-47)}`
                            : item.object_key}
                        </code>
                        <small className="field-help">{item.scope}</small>
                        {item.deletion_pending && (
                          <small className="field-help">Deletion pending</small>
                        )}
                      </td>
                      <td>{sourceLabel(item.source)}</td>
                      <td>{byteSize(item.bytes)}</td>
                      <td>{timestamp(item.created_at)}</td>
                      <td>
                        <div className="toolbar-actions">
                          {item.deletion_pending ? (
                            <Button size="sm" disabled>
                              Restore
                            </Button>
                          ) : (
                            <Link
                              className="button button-sm"
                              to="/backups/artifacts/$artifactId/restore"
                              params={{ artifactId: item.id }}
                            >
                              Restore
                            </Link>
                          )}
                          <Button
                            size="sm"
                            variant="ghost"
                            disabled={item.deletion_pending}
                            onClick={() => setRemove(item)}
                          >
                            Delete
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))
                : jobs.data?.items.map((item) => (
                    <tr key={item.id}>
                      <td>
                        <Link to="/backups/$jobId" params={{ jobId: item.id }}>
                          {item.kind === 'restore' ? 'Restore database' : 'Database backup'}
                        </Link>
                        <code className="field-help">{item.id.slice(0, 12)}</code>
                      </td>
                      <td>{sourceLabel(item.source)}</td>
                      <td>
                        <Status value={item.status} small />
                      </td>
                      <td>{timestamp(item.created_at)}</td>
                      <td>
                        <Link
                          className="button button-sm button-ghost"
                          to="/backups/$jobId"
                          params={{ jobId: item.id }}
                        >
                          Inspect
                          <Icon name="chevron" size={14} />
                        </Link>
                      </td>
                    </tr>
                  ))}
            </tbody>
          </table>
        </div>
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
      description="This permanently deletes the encrypted object from the configured bucket."
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
              <dt>Object</dt>
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
            <input
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
  const [remove, setRemove] = useState<BackupDestination | null>(null)
  const [testing, setTesting] = useState('')
  const [result, setResult] = useState('')
  const [error, setError] = useState('')
  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2>Object storage</h2>
          <p>Up to 32 encrypted backup destinations.</p>
        </div>
        <Link className="button button-primary" to="/backups/destinations/new">
          Add destination
        </Link>
      </div>
      {destinations.isPending ? (
        <Loading />
      ) : destinations.error ? (
        <ErrorState error={destinations.error} />
      ) : !destinations.data?.items.length ? (
        <Empty
          icon="archive"
          title="No destinations configured"
          description="Connect an S3-compatible bucket for encrypted database backups."
        />
      ) : (
        <div className="catalog-grid">
          {destinations.data.items.map((item) => (
            <section className="panel catalog-card" key={item.id}>
              <div className="title-row">
                <Icon name="archive" size={23} />
                <h2>{item.name}</h2>
              </div>
              <p className="break-text">{item.endpoint}</p>
              <dl className="service-definition-list">
                <div>
                  <dt>Bucket / prefix</dt>
                  <dd>
                    {item.bucket} / {item.prefix || 'root'}
                  </dd>
                </div>
                <div>
                  <dt>Region</dt>
                  <dd>{item.region}</dd>
                </div>
                <div>
                  <dt>Encryption</dt>
                  <dd>
                    <code title={item.encryption_recipient}>
                      {item.encryption_recipient.slice(0, 18)}…
                    </code>
                  </dd>
                </div>
              </dl>
              <div className="toolbar-actions">
                <Link
                  className="button button-sm"
                  to="/backups/destinations/$destinationId/edit"
                  params={{ destinationId: item.id }}
                >
                  Edit
                </Link>
                <Button
                  size="sm"
                  disabled={Boolean(testing)}
                  onClick={async () => {
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
                      setResult(value.message)
                    } catch (err) {
                      setError(message(err))
                    } finally {
                      setTesting('')
                    }
                  }}
                >
                  {testing === item.id ? 'Testing…' : 'Test connection'}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => setRemove(item)}>
                  Remove
                </Button>
              </div>
            </section>
          ))}
        </div>
      )}
      {result && <Note>{result}</Note>}
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
  const [remove, setRemove] = useState<BackupSchedule | null>(null)
  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2>Backup schedules</h2>
          <p>Recurring jobs with bounded retained backup counts.</p>
        </div>
        <Link className="button button-primary" to="/backups/schedules/new">
          Create schedule
        </Link>
      </div>
      {schedules.isPending ? (
        <Loading />
      ) : schedules.error ? (
        <ErrorState error={schedules.error} />
      ) : !schedules.data?.items.length ? (
        <Empty
          icon="clock"
          title="No schedules yet"
          description="Choose a database, destination, interval, and retention count."
        />
      ) : (
        <div className="panel settings-session-list">
          {schedules.data.items.map((item) => (
            <div className="settings-list-row" key={item.id}>
              <div>
                <strong>{item.name}</strong>
                <small>{sourceLabel(item.source)}</small>
                <small>
                  Every {item.interval_hours} hours · Keep {item.retention_count} ·{' '}
                  {item.enabled ? `Next ${timestamp(item.next_run_at)}` : 'Paused'}
                </small>
              </div>
              <div className="toolbar-actions">
                <Link
                  className="button button-sm"
                  to="/backups/schedules/$scheduleId/edit"
                  params={{ scheduleId: item.id }}
                >
                  Edit
                </Link>
                <Button size="sm" variant="ghost" onClick={() => setRemove(item)}>
                  Remove
                </Button>
              </div>
            </div>
          ))}
        </div>
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
        {error && <ErrorState error={error} />}
      </div>
      <div className="dialog-footer">
        <Button disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        <Button
          variant="danger"
          disabled={busy}
          onClick={async () => {
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
