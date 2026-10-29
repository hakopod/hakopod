import { Input } from '../components/ui/input'
import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import type { components } from '../lib/api.generated'
import { message, relative, timestamp } from '../lib/api'
import { duration, everyHours, until, useBackupNames, When } from '../components/backup-names'
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
  RequestError,
  Loading,
  Note,
  PageHeader,
  Status,
} from '../components/shared'
import {
  GuidedEmpty,
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
        <PageHeader title="Backups" />
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
  // Without object storage nothing can run or restore; the empty state adds storage first.
  const destinations = useBackupDestinations()
  const noStorage = Boolean(destinations.data && !destinations.data.items.length)
  return (
    <>
      <PageHeader
        eyebrow="DATA PROTECTION"
        title="Backups"
        description="Encrypted database backups kept in your own object storage, run by hand or on a schedule, and restored into a new database."
        action={
          tab === 'destinations' ? (
            <Button asChild variant="primary">
              <Link to="/backups/destinations/new">
                <Icon name="plus" size={15} />
                Add object storage
              </Link>
            </Button>
          ) : tab === 'schedules' ? (
            <Button asChild variant="primary">
              <Link to="/backups/schedules/new">
                <Icon name="plus" size={15} />
                Create schedule
              </Link>
            </Button>
          ) : noStorage ? null : (
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
  const names = useBackupNames()
  const schedules = useBackupSchedules()
  const jobs = useQuery({
    queryKey: ['backups', page.cursor],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/backups', { signal, params: { query: { cursor: page.cursor || undefined } } }),
      ),
    enabled: !artifacts,
    gcTime: 0,
    refetchInterval: (query) =>
      query.state.data?.items.some((item) => ['queued', 'running'].includes(item.status))
        ? 5000
        : false,
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
  const scheduleName = (id?: string) =>
    !id
      ? ''
      : schedules.data?.items.find((item) => item.id === id)?.name ||
        (schedules.isPending ? '(loading)' : schedules.error ? '(name unavailable)' : '(removed)')
  const noStorage = names.destinations.data && !names.destinations.data.items.length
  const title = artifacts ? 'Stored backups' : 'Job history'
  return (
    <section className="backup-section" aria-labelledby="backup-history-heading">
      <div className="section-toolbar">
        <div className="hako-section-heading-title">
          <h2 id="backup-history-heading">{title}</h2>
          <HeadingHelp title={title}>
            {artifacts
              ? 'Encrypted backups kept in object storage, newest first. Restoring creates a new database; it never overwrites one.'
              : 'Every backup and restore, newest first, with its outcome. Up to 100 per page.'}
          </HeadingHelp>
        </div>
        <Button
          size="sm"
          variant="ghost"
          disabled={active.isFetching}
          onClick={() => void active.refetch()}
        >
          <Icon name="refresh" size={14} className={active.isFetching ? 'spin' : ''} />
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
        noStorage ? (
          <GuidedEmpty
            icon="archive"
            satellites={[
              { icon: 'database', label: 'Database' },
              { icon: 'server', label: 'Bucket' },
              { icon: 'clock', label: 'Schedule' },
              { icon: 'refresh', label: 'Restore' },
            ]}
            title="Set up backups"
            description="Backups are encrypted and written to an S3-compatible bucket you own. Add one bucket, then back up any database on a schedule."
            primary={
              <Button asChild variant="primary">
                <Link to="/backups/destinations/new">
                  <Icon name="plus" size={14} />
                  Add object storage
                </Link>
              </Button>
            }
            steps={[
              {
                title: 'Add a bucket',
                detail: 'The endpoint and credentials are checked when you save.',
              },
              {
                title: 'Schedule a database',
                detail: 'Pick how often to back up and how long to keep copies.',
              },
              { title: 'Restore when needed', detail: 'Restore any stored copy into a database.' },
            ]}
          />
        ) : (
          <GuidedEmpty
            compact
            icon="archive"
            title={artifacts ? 'No stored backups yet' : 'No backup has run yet'}
            description={
              artifacts
                ? 'Each successful backup is kept here until you delete it or its schedule’s retention removes it.'
                : 'Run a backup by hand now, or let a schedule back up a database on an interval.'
            }
            secondary={
              <Button asChild>
                <Link to="/backups/schedules/new">Create schedule</Link>
              </Button>
            }
          />
        )
      ) : (
        <>
          {active.error && (
            <StaleNotice updatedAt={active.dataUpdatedAt} retry={() => void active.refetch()} />
          )}
          <ResourceRows label={title} className="backup-rows">
            {artifacts
              ? stored.data?.items.map((item) => (
                  <ResourceRow
                    key={item.id}
                    identity={<span>{names.source(item.source)}</span>}
                    detail={
                      <>
                        <span>{byteSize(item.bytes)}</span>
                        <span>in {names.destination(item.destination_id)}</span>
                        <code title={item.object_key}>
                          {item.object_key.length > 40
                            ? `…${item.object_key.slice(-39)}`
                            : item.object_key}
                        </code>
                        <span>
                          {item.schedule_id
                            ? `By schedule ${scheduleName(item.schedule_id)}`
                            : 'Run by hand'}
                        </span>
                        {engineManaged(item) && <span>Size reported by the engine</span>}
                      </>
                    }
                    status={
                      item.deletion_pending ? (
                        <Status value="deletion pending" small />
                      ) : (
                        <span className="resource-row-count">{names.engine(item.source)}</span>
                      )
                    }
                    meta={<When date={item.created_at} />}
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
                              aria-label={`Restore backup of ${names.source(item.source)} from ${timestamp(item.created_at)}`}
                            >
                              Restore
                            </Link>
                          </Button>
                        )}
                        <Button
                          size="icon"
                          variant="ghost"
                          disabled={item.deletion_pending}
                          aria-label={`Delete backup of ${names.source(item.source)} from ${timestamp(item.created_at)}`}
                          onClick={() => setRemove(item)}
                        >
                          <Icon name="trash" size={15} />
                        </Button>
                      </>
                    }
                  />
                ))
              : jobs.data?.items.map((item) => {
                  const running = ['queued', 'running'].includes(item.status)
                  const what =
                    item.kind === 'restore'
                      ? `Restore into ${item.target?.managed_database_name || item.target?.application_name || (item.target ? sourceLabel(item.target) : 'a new database')}`
                      : `Backup of ${names.source(item.source)}`
                  return (
                    <ResourceRow
                      key={item.id}
                      identity={
                        <Link
                          className="resource-row-link"
                          to="/backups/$jobId"
                          params={{ jobId: item.id }}
                        >
                          {what}
                        </Link>
                      }
                      detail={
                        <>
                          <span>
                            {item.kind === 'restore' ? 'from' : 'to'}{' '}
                            {names.destination(item.destination_id)}
                          </span>
                          {item.status === 'succeeded' && <span>{byteSize(item.bytes)}</span>}
                          {item.finished_at ? (
                            <span>took {duration(item.started_at, item.finished_at)}</span>
                          ) : (
                            item.started_at &&
                            running && (
                              <span>
                                running {duration(item.started_at, new Date().toISOString())}
                              </span>
                            )
                          )}
                          <span>
                            {item.schedule_id
                              ? `Schedule ${scheduleName(item.schedule_id)}`
                              : 'Run by hand'}
                          </span>
                          {item.status === 'failed' && item.error && (
                            <span className="backup-row-error" title={item.error}>
                              {item.error.split('\n')[0]}
                            </span>
                          )}
                        </>
                      }
                      status={
                        <Status
                          value={running && item.cancel_requested ? 'cancelling' : item.status}
                          small
                        />
                      }
                      meta={<When date={item.created_at} />}
                    />
                  )
                })}
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
            Newer
          </Button>
          <span>Page {page.previous.length + 1}</span>
          <Button
            size="sm"
            disabled={!next || page.previous.length >= 20}
            onClick={() =>
              next && setPage({ cursor: next, previous: [...page.previous, page.cursor] })
            }
          >
            Older
          </Button>
        </div>
      )}
      {removal && <RemoveArtifact artifact={removal} onClose={() => setRemove(null)} />}
    </section>
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
  const [results, setResults] = useState<Record<string, { ok: boolean; text: string }>>({})
  async function test(item: BackupDestination) {
    if (testing) return
    setTesting(item.id)
    try {
      const value = await unwrap(
        client.POST('/backup-destinations/{id}/test', {
          params: { path: { id: item.id } },
          body: {},
        }),
      )
      setResults((previous) => ({ ...previous, [item.id]: { ok: true, text: value.message } }))
    } catch (err) {
      setResults((previous) => ({ ...previous, [item.id]: { ok: false, text: message(err) } }))
    } finally {
      setTesting('')
    }
  }
  return (
    <section className="backup-section" aria-labelledby="backup-storage-heading">
      <div className="section-toolbar">
        <div className="hako-section-heading-title">
          <h2 id="backup-storage-heading">Object storage</h2>
          <HeadingHelp title="Object storage">
            S3-compatible buckets that hold encrypted backups. Credentials stay on the server.
          </HeadingHelp>
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
        <GuidedEmpty
          compact
          icon="server"
          title="Add a bucket for backups"
          description="Backups need an S3-compatible bucket. Nothing can be backed up until one is added."
          steps={[
            { title: 'Enter the endpoint and bucket', detail: 'Any S3-compatible service.' },
            { title: 'Add credentials', detail: 'Stored on the server, never shown again.' },
          ]}
        />
      ) : (
        <ResourceRows label="Object storage destinations" className="backup-rows">
          {destinations.data.items.map((item) => {
            const result = results[item.id]
            return (
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
                    <code>{[item.bucket, item.prefix].filter(Boolean).join('/')}</code>
                    <span>{item.endpoint.replace(/^https?:\/\//, '')}</span>
                    <span>{item.region}</span>
                    <span className="copy-id">
                      <span>Key</span>
                      <code title={item.encryption_recipient}>
                        {item.encryption_recipient.slice(0, 14)}…
                      </code>
                      <Copy
                        value={item.encryption_recipient}
                        label={`Copy encryption recipient for ${item.name}`}
                        iconOnly
                      />
                    </span>
                    {result && (
                      <span
                        role="status"
                        className={result.ok ? undefined : 'backup-row-error'}
                        title={result.text}
                      >
                        {result.text}
                      </span>
                    )}
                  </>
                }
                status={
                  testing === item.id ? (
                    <Status value="testing" small />
                  ) : result ? (
                    <Status value={result.ok ? 'reachable' : 'failed'} small />
                  ) : (
                    <span className="resource-row-count">Not tested</span>
                  )
                }
                actions={
                  <>
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={Boolean(testing)}
                      onClick={() => void test(item)}
                      aria-label={`Test connection to ${item.name}`}
                    >
                      {testing === item.id ? 'Testing…' : 'Test'}
                    </Button>
                    <RowActions
                      label={item.name}
                      actions={[
                        {
                          label: 'Edit name and credentials',
                          run: () =>
                            void navigate({
                              to: '/backups/destinations/$destinationId/edit',
                              params: { destinationId: item.id },
                            }),
                        },
                        {
                          label: 'Remove object storage',
                          run: () => setRemove(item),
                          destructive: true,
                        },
                      ]}
                    />
                  </>
                }
              />
            )
          })}
        </ResourceRows>
      )}
      {remove && (
        <RemoveBackupConfig
          kind="destination"
          id={remove.id}
          name={remove.name}
          revision={remove.revision}
          onClose={() => setRemove(null)}
        />
      )}
    </section>
  )
}
function Schedules() {
  const schedules = useBackupSchedules()
  const names = useBackupNames()
  const navigate = useNavigate()
  // The newest page of jobs shows each schedule's latest run without another endpoint.
  const jobs = useQuery({
    queryKey: ['backups', ''],
    queryFn: ({ signal }) => unwrap(client.GET('/backups', { signal })),
    gcTime: 0,
  })
  const [remove, setRemove] = useState<BackupSchedule | null>(null)
  return (
    <section className="backup-section" aria-labelledby="backup-schedules-heading">
      <div className="section-toolbar">
        <div className="hako-section-heading-title">
          <h2 id="backup-schedules-heading">Schedules</h2>
          <HeadingHelp title="Schedules">
            Each schedule backs up one database on an interval and keeps its newest backups.
          </HeadingHelp>
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
        <GuidedEmpty
          compact
          icon="clock"
          title="Back up automatically"
          description="Nothing is backed up automatically yet. A schedule backs up one database on an interval."
          steps={[
            { title: 'Pick a database', detail: 'Choose what to back up.' },
            { title: 'Set the interval', detail: 'How often to back up.' },
            { title: 'Set retention', detail: 'How many copies to keep.' },
          ]}
        />
      ) : (
        <ResourceRows label="Backup schedules" className="backup-rows">
          {schedules.data.items.map((item) => {
            const last = jobs.data?.items.find((job) => job.schedule_id === item.id)
            return (
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
                    <span>
                      {names.source(item.source)} → {names.destination(item.destination_id)}
                    </span>
                    <span>
                      {everyHours(item.interval_hours).replace(/^./, (c) => c.toUpperCase())} ·
                      keeps newest {item.retention_count}
                    </span>
                    {jobs.isPending ? null : last ? (
                      <Link
                        to="/backups/$jobId"
                        params={{ jobId: last.id }}
                        className={last.status === 'failed' ? 'backup-row-error' : undefined}
                      >
                        Last run {last.status} {relative(last.created_at).toLowerCase()}
                      </Link>
                    ) : (
                      <span>
                        {jobs.error ? 'Last run unavailable' : 'No run in recent history'}
                      </span>
                    )}
                  </>
                }
                status={<Status value={item.enabled ? 'active' : 'paused'} small />}
                meta={
                  item.enabled ? (
                    <time dateTime={item.next_run_at} title={timestamp(item.next_run_at)}>
                      Next {until(item.next_run_at)}
                    </time>
                  ) : (
                    'No next run'
                  )
                }
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
            )
          })}
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
    </section>
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
          ? 'Hakopod forgets this bucket and its credentials. Removal is refused while stored backups, schedules or running jobs still use it.'
          : 'No more backups run on this schedule.'
      }
    >
      <div className="dialog-body">
        <Note>
          {kind === 'destination'
            ? 'Delete its stored backups and schedules first. Objects outside Hakopod’s records in the bucket are not touched.'
            : 'Backups this schedule already made stay stored until you delete them.'}
        </Note>
        {error && <RequestError error={error} />}
      </div>
      <div className="dialog-footer">
        <Button disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        <Button
          variant="danger"
          disabled={busy}
          onClick={async () => {
            if (busy) return
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
          {busy
            ? 'Removing…'
            : `Remove ${kind === 'destination' ? 'object storage' : 'schedule'} ${name}`}
        </Button>
      </div>
    </Dialog>
  )
}
