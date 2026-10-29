import { useState, type ReactNode } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, relative, timestamp } from '../lib/api'
import {
  databaseHealth,
  useDatabaseConnections,
  type DatabaseOperation,
  type ManagedDatabase,
} from '../lib/databases'
import {
  databaseLayoutSummary,
  databaseStorageGiB,
  endpointAddress,
  endpointName,
  engineName,
  expectedMembers,
  votingDatabase,
} from '../lib/database-view'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Input } from './ui/input'
import { Icon } from './icons'
import { Copy, RequestError, Status } from './shared'
import { ResourceEmpty, ResourceRow, ResourceRows } from './resource-card'
import { CopyValue, ReviewFacts, ReviewNotice, reviewDialog } from './operation-review'

const observed = (d: ManagedDatabase) =>
  d.observation.observed_at && !d.observation.observed_at.startsWith('0001-')
    ? d.observation.observed_at
    : undefined

/** One status word for a database: lifecycle first, then observation, with stale kept distinct. */
export function databaseState(d: ManagedDatabase, now = Date.now()) {
  const health = databaseHealth(d, now)
  if (d.status !== 'ready') return d.status
  if (health === 'Observation stale') return 'stale'
  if (health === 'Not observed') return 'not observed'
  return health
}

export function readyMembers(d: ManagedDatabase, now = Date.now()) {
  const health = databaseHealth(d, now)
  if (health === 'Not observed') return undefined
  return `${(d.observation.members || []).filter((m) => m.ready).length} of ${expectedMembers(d.spec)}`
}

/** Status label, readiness and freshness in one line, the first thing on the page. */
export function DatabaseHealthLine({ database: d, now }: { database: ManagedDatabase; now: number }) {
  const state = databaseState(d, now)
  const ready = readyMembers(d, now)
  const stale = databaseHealth(d, now) === 'Observation stale'
  const at = observed(d)
  return (
    <p className="db-health-line">
      <Status value={state} small />
      <span>
        {ready
          ? `${ready} members ready${stale ? ' · last known' : ''}`
          : `${expectedMembers(d.spec)} ${expectedMembers(d.spec) === 1 ? 'member' : 'members'} desired`}
      </span>
      <span aria-hidden="true">·</span>
      {at ? (
        <time dateTime={at} title={timestamp(at)}>
          {stale ? 'Last observed' : 'Observed'} {relative(at).toLowerCase()}
        </time>
      ) : (
        <span>Not observed yet</span>
      )}
    </p>
  )
}

export function useDatabaseFailures(id: string) {
  return useQuery({
    queryKey: ['database-failures', id],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/databases/{id}/failures', { signal, params: { path: { id } } })),
    refetchInterval: 30_000,
    staleTime: 15_000,
    gcTime: 0,
  })
}

const opLabel = (kind: string) => kind.replaceAll('-', ' ').replaceAll('_', ' ')
const sentence = (text: string) => text.charAt(0).toUpperCase() + text.slice(1)

/** What is wrong, the recorded cause, and the specific next step. Hidden when nothing needs attention. */
export function DatabaseAttention({
  database: d,
  now,
  refreshFailed,
  latest,
  canManage,
  onRefresh,
  onTab,
}: {
  database: ManagedDatabase
  now: number
  refreshFailed: boolean
  latest?: DatabaseOperation
  canManage: boolean
  onRefresh: () => void
  onTab: (tab: 'activity' | 'connections' | 'monitoring') => void
}) {
  const failures = useDatabaseFailures(d.id)
  const state = databaseState(d, now)
  const at = observed(d)
  const tls = d.observation.tls
  const tlsExpired =
    d.spec.tls?.mode === 'required' &&
    Number.isFinite(Date.parse(tls?.expires_at || '')) &&
    Date.parse(tls!.expires_at!) <= now
  const latestFailed = latest?.status === 'failed' && latest.revision === d.revision ? latest : undefined
  const running = latest && !latest.finished_at && latest.status !== 'failed' ? latest : undefined
  const stale = databaseHealth(d, now) === 'Observation stale'
  const recent = (failures.data?.items || []).filter((f) => f.revision === d.revision).slice(0, 3)
  const memoryLimited = recent.some((f) => f.memory_limit_bytes || /oom/i.test(f.reason))
  const search = { project: d.project, environment: d.environment }
  const pending = ['pending', 'provisioning', 'creating', 'not observed'].includes(state)
  const failing = ['failed', 'error', 'unavailable', 'degraded', 'not ready'].includes(state)
  if (!failing && !pending && !stale && !latestFailed && !recent.length && !tlsExpired && !refreshFailed)
    return null
  const title = latestFailed
    ? `${sentence(opLabel(latestFailed.kind))} failed at revision ${latestFailed.revision}`
    : failing
      ? `${d.spec.name} is ${state}`
      : stale
        ? 'The last observation is stale'
        : pending
          ? running
            ? `${sentence(opLabel(running.kind))} in progress${running.phase ? ` · ${running.phase}` : ''}`
            : at
              ? `${d.spec.name} is ${state}`
              : `Waiting for ${d.spec.name} to be observed`
          : tlsExpired
            ? 'The server certificate has expired'
            : recent.length
              ? `${recent.length} recent ${recent.length === 1 ? 'failure' : 'failures'} at revision ${d.revision}`
              : 'Refresh failed'
  const tone = failing || latestFailed || tlsExpired ? 'error' : stale || refreshFailed || recent.length ? 'warn' : 'info'
  const retryResize =
    canManage && latestFailed && ['resize', 'resize-retry'].includes(latestFailed.kind) &&
    votingDatabase(d.spec.engine) && d.spec.mode === 'cluster' && d.status === 'failed'
  const retrySwitchover =
    canManage && latestFailed?.kind === 'switchover' && latestFailed.switchover && latestFailed.phase !== 'switchover'
  return (
    <section className={`app-attention db-attention db-attention-${tone}`} aria-labelledby="db-attention-heading">
      <Icon name={tone === 'info' ? 'info' : 'alert'} size={18} />
      <div className="app-attention-body">
        <h2 id="db-attention-heading">{title}</h2>
        {latestFailed?.message && <p className="app-attention-log"><span className="sr-only">Recorded error: </span><code>{latestFailed.message}</code></p>}
        {d.observation.message && d.observation.message !== latestFailed?.message && (
          <p className="app-attention-detail">{d.observation.message}</p>
        )}
        {running?.message && pending && <p className="app-attention-detail">{running.message}</p>}
        {stale && (
          <p className="app-attention-detail">
            {failing ? 'This observation is stale. ' : ''}Last observed {at ? relative(at).toLowerCase() : 'never'}
            {d.observation.revision !== d.revision ? ` at revision ${d.observation.revision}, desired revision ${d.revision}` : ''}.
            Members and topology show the last known state.
          </p>
        )}
        {pending && !at && (
          <p className="app-attention-detail">
            No member, endpoint or metric has been reported yet. {expectedMembers(d.spec)}{' '}
            {expectedMembers(d.spec) === 1 ? 'member is' : 'members are'} desired.
          </p>
        )}
        {tlsExpired && (
          <p className="app-attention-detail">Certificate expired {timestamp(tls!.expires_at)}. Clients that verify TLS will refuse to connect.</p>
        )}
        {memoryLimited && d.status !== 'ready' && (
          <p className="app-attention-detail">
            A member ran out of memory. Capacity changes need a ready database; review a recovery
            into a larger database if it does not recover.
          </p>
        )}
        {refreshFailed && <p className="app-attention-detail">Refresh failed. Showing the last received observation.</p>}
        {recent.length > 0 && (
          <ul className="db-attention-failures" aria-label="Recent failures">
            {recent.map((f) => (
              <li key={`${f.member_uid}-${f.container}-${f.occurred_at}`}>
                <strong>{f.summary}</strong>
                <span>
                  {f.member} · {f.container}
                  {f.exit_code !== undefined && ` · exit ${f.exit_code}`}
                  {f.restart_count !== undefined && ` · ${f.restart_count} restarts`} ·{' '}
                  <time dateTime={f.occurred_at} title={timestamp(f.occurred_at)}>{relative(f.occurred_at).toLowerCase()}</time>
                </span>
              </li>
            ))}
          </ul>
        )}
        <div className="app-attention-actions">
          {retryResize && (
            <Button size="sm" asChild>
              <Link to="/databases/$databaseId/resize-retry" params={{ databaseId: d.id }} search={{ ...search, operation: latestFailed!.id }}>
                Review resize retry
              </Link>
            </Button>
          )}
          {retrySwitchover && (
            <Button size="sm" asChild>
              <Link to="/databases/$databaseId/switchover" params={{ databaseId: d.id }} search={{ ...search, operation: latestFailed!.phase === 'review' ? undefined : latestFailed!.id }}>
                {latestFailed!.phase === 'review' ? 'Review current topology' : 'Review switchover retry'}
              </Link>
            </Button>
          )}
          {memoryLimited && canManage && d.status === 'ready' && (
            <Button size="sm" asChild>
              <Link to="/databases/$databaseId/resize" params={{ databaseId: d.id }} search={search}>
                Review capacity
              </Link>
            </Button>
          )}
          {latestFailed && (
            <Button size="sm" onClick={() => onTab('activity')}>
              Inspect {opLabel(latestFailed.kind)}
            </Button>
          )}
          {running && pending && (
            <Button size="sm" onClick={() => onTab('activity')}>
              Inspect {opLabel(running.kind)}
            </Button>
          )}
          {tlsExpired && (
            <Button size="sm" onClick={() => onTab('connections')}>
              Inspect connection security
            </Button>
          )}
          {(stale || refreshFailed) && (
            <Button size="sm" onClick={onRefresh}>
              <Icon name="refresh" size={14} />
              Refresh now
            </Button>
          )}
        </div>
      </div>
    </section>
  )
}

/** Private endpoints with a named copy control each. */
export function DatabaseEndpoints({ database: d }: { database: ManagedDatabase }) {
  const endpoints = d.observation.endpoints || []
  if (!endpoints.length)
    return (
      <ResourceEmpty
        icon="network"
        title="No verified endpoint"
        description={
          d.status === 'ready'
            ? 'The database has not reported a private endpoint.'
            : 'Endpoints appear after the database is observed ready.'
        }
      />
    )
  return (
    <ResourceRows label="Private endpoints" className="db-endpoint-rows">
      {endpoints.map((e) => {
        const name = endpointName(e.purpose, d.spec.engine)
        const address = endpointAddress(e, d.spec.engine)
        return (
          <ResourceRow
            key={e.purpose}
            identity={<span>{name}</span>}
            detail={<code title={address}>{address}</code>}
            actions={<CopyValue value={address} what={`${name} address`} />}
          />
        )
      })}
    </ResourceRows>
  )
}

/** The desired shape, scope and identity of the database, next to what was observed. */
export function DatabaseDetails({ database: d }: { database: ManagedDatabase }) {
  const nodes = [...new Set((d.observation.members || []).map((m) => m.node).filter(Boolean))]
  const rows: [string, ReactNode][] = [
    ['Engine', `${engineName(d.spec.engine)} ${d.spec.version}${['clickhouse', 'oracle'].includes(d.spec.engine) ? ' · preview' : ''}`],
    ['Layout', databaseLayoutSummary(d.spec)],
    ['Per member', `${d.spec.cpu} CPU · ${d.spec.memory}`],
    ['Configured storage', `${databaseStorageGiB(d.spec)} GiB · usage not measured`],
    ['Placement', nodes.length ? `${nodes.length} ${nodes.length === 1 ? 'node' : 'nodes'}${databaseHealth(d) === 'Observation stale' ? ' · last known' : ''}` : 'Not observed'],
    ['Client TLS', d.spec.tls?.mode === 'required' ? 'Required' : 'Not enforced'],
    ['Created', timestamp(d.created_at)],
    [
      'Database ID',
      <span className="db-id" key="id">
        <code title={d.id}>{d.id.slice(0, 8)}</code>
        <Copy iconOnly value={d.id} label="Copy database ID" />
      </span>,
    ],
  ]
  return (
    <dl className="app-workspace-details">
      {rows.map(([term, value]) => (
        <div key={term}>
          <dt>{term}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  )
}

type OperationRoute =
  | '/databases/$databaseId/connect'
  | '/databases/$databaseId/provision-application'
  | '/databases/$databaseId/query'
  | '/databases/$databaseId/resize'
  | '/databases/$databaseId/switchover'
  | '/databases/$databaseId/recover'
  | '/databases/$databaseId/public-endpoints'
  | '/databases/$databaseId/private-access'
  | '/databases/$databaseId/migration-lock-recovery'
type Operation = {
  name: string
  consequence: string
  to?: OperationRoute
  label: string
  unavailable?: string
  destructive?: boolean
  onSelect?: () => void
}

/** Every supported change as a named row that opens its own review. */
export function DatabaseOperations({
  database: d,
  canManage,
  canQuery,
}: {
  database: ManagedDatabase
  canManage: boolean
  canQuery: boolean
}) {
  const [deleting, setDeleting] = useState(false)
  const search = { project: d.project, environment: d.environment }
  const ready = d.status === 'ready'
  const postgres = d.spec.engine === 'postgresql'
  const notReady = ready ? undefined : `Available when the database is ready (now ${d.status})`
  const access = canManage ? undefined : 'Requires deployment access to this project'
  const capacity = votingDatabase(d.spec.engine)
    ? d.spec.mode === 'cluster'
      ? 'Change replicas'
      : 'Change capacity'
    : ['clickhouse', 'oracle', 'vitess', 'duckdb'].includes(d.spec.engine)
      ? 'Change capacity'
      : 'Resize'
  const operations: (Operation | false)[] = [
    {
      name: 'Connect an application',
      consequence: 'Saves a connection on a service and queues a reviewed redeployment.',
      to: '/databases/$databaseId/connect',
      label: 'Connect application',
      unavailable: access || notReady,
    },
    postgres && {
      name: 'Create an application database',
      consequence: 'Creates a scoped login and logical database, then deploys the reviewed binding.',
      to: '/databases/$databaseId/provision-application',
      label: 'Review application database',
      unavailable: access || notReady,
    },
    postgres && {
      name: 'Run a SQL query',
      consequence: 'Runs one statement with a 20-second deadline and a 256 KiB result limit.',
      to: '/databases/$databaseId/query',
      label: 'Open SQL query',
      unavailable: canQuery ? notReady : 'Requires database query permission',
    },
    {
      name: capacity,
      consequence: 'Shows the new size and its impact for review before anything changes.',
      to: '/databases/$databaseId/resize',
      label: `Review ${capacity.toLowerCase()}`,
      unavailable: access || notReady,
    },
    d.spec.engine === 'oracle' && d.spec.oracle?.edition === 'enterprise' && d.spec.mode === 'cluster' && {
      name: 'Switch primary',
      consequence: 'Reviews the current topology before the primary role moves.',
      to: '/databases/$databaseId/switchover',
      label: 'Review switchover',
      unavailable: access || notReady,
    },
    {
      name: 'Recover a backup into this database',
      consequence: 'Restores an archive into this empty database. The source database stays available.',
      to: '/databases/$databaseId/recover',
      label: 'Review recovery',
      unavailable:
        access ||
        notReady ||
        (d.revision !== 1 || d.recovery ? 'Only an unused database at revision 1 can be a recovery target' : undefined),
    },
    postgres && {
      name: 'Public endpoints',
      consequence: 'Publishes a restricted listener from operator address and port inventory.',
      to: '/databases/$databaseId/public-endpoints',
      label: 'Manage public endpoints',
      unavailable: access,
    },
    {
      name: 'Private connection from your machine',
      consequence: 'Generates network and certificate steps for a client. No connection is opened.',
      to: '/databases/$databaseId/private-access',
      label: 'Open private connection',
    },
    postgres && {
      name: 'Recover a migration lock',
      consequence: 'Releases an abandoned migration lock after fresh evidence is reviewed.',
      to: '/databases/$databaseId/migration-lock-recovery',
      label: 'Review lock recovery',
      unavailable: access || notReady,
    },
    {
      name: 'Delete database',
      consequence: 'Removes every member and its data. Saved backups remain.',
      label: 'Review deletion',
      destructive: true,
      unavailable: access,
      onSelect: () => setDeleting(true),
    },
  ]
  return (
    <>
      <ResourceRows label="Database operations" className="db-operation-rows">
        {operations
          .filter((op): op is Operation => Boolean(op))
          .map((op) => (
            <ResourceRow
              key={op.name}
              identity={<span>{op.name}</span>}
              detail={
                <>
                  <span>{op.consequence}</span>
                  {op.unavailable && <span className="db-operation-unavailable">{op.unavailable}</span>}
                </>
              }
              actions={
                op.unavailable ? (
                  <Button size="sm" disabled>
                    {op.destructive && <Icon name="trash" size={14} />}
                    {op.label}
                  </Button>
                ) : op.to ? (
                  <Button size="sm" asChild>
                    <Link to={op.to} params={{ databaseId: d.id }} search={search}>
                      {op.label}
                    </Link>
                  </Button>
                ) : (
                  <Button size="sm" onClick={op.onSelect}>
                    {op.destructive && <Icon name="trash" size={14} />}
                    {op.label}
                  </Button>
                )
              }
            />
          ))}
      </ResourceRows>
      {deleting && <DeleteDatabaseReview database={d} onClose={() => setDeleting(false)} />}
    </>
  )
}

/** Deletion is severe and irreversible, so the name is typed once in the review. */
export function DeleteDatabaseReview({ database: d, onClose }: { database: ManagedDatabase; onClose: () => void }) {
  const navigate = useNavigate()
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const members = (d.observation.members || []).length
  const connections = useDatabaseConnections(d.id)
  const bound = [
    ...new Set((connections.data?.items || []).map((c) => c.application_display_name || c.application_name)),
  ]
  return (
    <Dialog
      open
      className={reviewDialog}
      onOpenChange={(open) => !open && !busy && onClose()}
      title={`Delete ${d.spec.name}?`}
      description="Deletion removes the database and its data volumes."
    >
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          setBusy(true)
          setError('')
          try {
            await unwrap(
              client.DELETE('/databases/{id}', {
                params: { path: { id: d.id }, header: { 'Idempotency-Key': crypto.randomUUID() } },
                body: { expected_revision: d.revision, confirm_name: confirmation },
              }),
            )
            void navigate({ to: '/databases', search: { project: d.project, environment: d.environment } })
          } catch (err) {
            setError(message(err))
            setBusy(false)
          }
        }}
      >
        <div className="dialog-body">
          <ReviewFacts
            facts={[
              ['Scope', `${d.project} / ${d.environment}`],
              ['Database', `${engineName(d.spec.engine)} ${d.spec.version} · ${databaseLayoutSummary(d.spec)}`],
              ['Members removed', `${members} observed · ${expectedMembers(d.spec)} desired`],
              ['Data removed', `${databaseStorageGiB(d.spec)} GiB of configured volumes`],
              [
                'Bound applications',
                connections.isPending
                  ? 'Loading…'
                  : connections.error
                    ? 'Unavailable'
                    : bound.length
                      ? bound.join(', ')
                      : 'None recorded',
              ],
              ['Revision', String(d.revision)],
            ]}
          />
          <ReviewNotice tone="danger" title="Data cannot be recovered from this database">
            Saved backups remain and can be recovered into a new database.
            {bound.length > 0 && ` ${bound.length === 1 ? 'The bound application loses its' : `${bound.length} bound applications lose their`} connection.`}
          </ReviewNotice>
          <label className="db-confirm-field">
            <span>Type {d.spec.name} to confirm</span>
            <Input value={confirmation} autoComplete="off" onChange={(e) => setConfirmation(e.target.value)} />
          </label>
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button type="button" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            variant="danger"
            aria-busy={busy || undefined}
            disabled={busy || confirmation !== d.spec.name}
          >
            <Icon name="trash" size={14} />
            {busy ? 'Deleting…' : `Delete ${d.spec.name} and data`}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

/** Recorded operations, newest first, each with its stages behind a disclosure. */
export function DatabaseActivity({
  database: d,
  operations,
  limit,
}: {
  database: ManagedDatabase
  operations: DatabaseOperation[]
  limit?: number
}) {
  const shown = limit ? operations.slice(0, limit) : operations
  if (!shown.length)
    return <ResourceEmpty icon="activity" title="No operations recorded" description="Changes to this database appear here." />
  return (
    <ResourceRows label="Database operations history" className="db-activity-rows">
      {shown.map((op) => (
        <ResourceRow
          key={op.id}
          identity={<span className="db-activity-kind">{opLabel(op.kind)}</span>}
          detail={
            <>
              <span>
                Revision {op.revision}
                {op.phase && ` · ${op.phase}`}
                {op.finished_at && ` · finished ${timestamp(op.finished_at)}`}
                {op.revision === d.revision && op.id === operations[0]?.id && ' · current'}
              </span>
              {op.message && (
                <span className={`db-activity-message${op.status === 'failed' ? ' db-activity-error' : ''}`}>{op.message}</span>
              )}
              {op.progress?.length > 0 && !limit && (
                <details className="db-activity-stages" open={op.status === 'failed'}>
                  <summary>{op.progress.length} {op.progress.length === 1 ? 'stage' : 'stages'}</summary>
                  <ol>
                    {op.progress.map((stage) => (
                      <li key={`${stage.id}-${stage.observed_at}`}>
                        <span>{stage.id.replaceAll('-', ' ')}</span>
                        <Status value={stage.state} small />
                        <time dateTime={stage.observed_at}>{timestamp(stage.observed_at)}</time>
                        {stage.message && <p>{stage.message}</p>}
                      </li>
                    ))}
                  </ol>
                </details>
              )}
            </>
          }
          status={<Status value={op.status} small />}
          meta={
            <time dateTime={op.created_at} title={timestamp(op.created_at)}>
              {relative(op.created_at)}
            </time>
          }
        />
      ))}
    </ResourceRows>
  )
}
