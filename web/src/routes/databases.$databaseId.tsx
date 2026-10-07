import { DatabaseInspection } from '../components/database-inspection'
import { useEffect, useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { useActiveSection } from '../lib/use-active-section'
import { createFileRoute, Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { client, unwrap } from '../lib/client'
import {
  useDatabase,
  databaseSummary,
  databaseHealth,
  type ManagedDatabase,
  useDatabaseOperations,
} from '../lib/databases'
import { useResourceScope, useScope, canAccess } from '../lib/scope'
import { message, timestamp } from '../lib/api'
import { Empty, ErrorState, Loading, Note, PageHeader } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Icon } from '../components/icons'
import {
  DatabaseIdentity,
  DatabaseSummary,
  DatabaseTopology,
  DatabaseMonitoring,
  DatabaseMembers,
} from '../components/database-cockpit'
import { DatabaseBackups } from '../components/database-backups'
import { DatabaseSecurity } from '../components/database-security'
import { Copy, Status } from '../components/shared'
import { endpointName, endpointAddress, votingDatabase } from '../lib/database-view'

const databaseTabs = [
  'overview',
  'monitoring',
  'connections',
  'backups',
  'activity',
  'settings',
] as const
type DatabaseTab = (typeof databaseTabs)[number]
export const Route = createFileRoute('/databases/$databaseId')({
  validateSearch: (search: Record<string, unknown>): { tab?: DatabaseTab } => ({
    tab: databaseTabs.includes(search.tab as DatabaseTab) ? (search.tab as DatabaseTab) : undefined,
  }),
  component: DetailRoute,
})
function DetailRoute() {
  const id = Route.useParams().databaseId
  return useLocation().pathname === `/databases/${id}` ? <Detail key={id} /> : <Outlet />
}
function Detail() {
  const tab = Route.useSearch().tab || 'overview'
  const navigationRoot = useActiveSection(tab, '.tab-list')
  const id = Route.useParams().databaseId
  const [tick, setNow] = useState(Date.now)
  const now = Math.max(tick, Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 5000)
    return () => clearInterval(timer)
  }, [])
  const query = useDatabase(id)
  useResourceScope(query.data)
  const operations = useDatabaseOperations(id, tab === 'activity' && Boolean(query.data))
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [confirmation, setConfirmation] = useState('')
  const navigate = useNavigate()
  const { identity } = useScope()
  if (query.isPending) return <Loading />
  if (query.error && !query.data) return <ErrorState error={query.error} />
  const d = query.data!
  const canManage = !identity.application && canAccess(identity, d.project, 'deployments:write')
  const canQuery = !identity.application && ['databases:query', 'databases:write-query'].some((permission) => canAccess(identity,d.project,permission) && (identity.credential_type !== 'machine' || (identity.project === d.project && identity.environment === d.environment && identity.permissions.includes(permission))))
  const search = { project: d.project, environment: d.environment }
  const canBackups = identity.admin || identity.can_manage_backups
  return (
    <div className="ops-page">
      <PageHeader
        title={d.spec.name}
        description={databaseSummary(d.spec)}
        action={
          <div className="flex flex-wrap gap-2">
            {canQuery && d.spec.engine === 'postgresql' && d.status === 'ready' && (
              <Button asChild><Link to="/databases/$databaseId/query" params={{ databaseId: id }} search={search}>SQL query</Link></Button>
            )}
            {canManage && d.status === 'ready' && (
              <Button asChild variant="primary">
                <Link
                  to="/databases/$databaseId/connect"
                  params={{ databaseId: id }}
                  search={search}
                >
                  Connect application
                </Link>
              </Button>
            )}
            {canManage && tab === 'settings' && d.status === 'ready' && (
              <Button asChild>
                <Link
                  to="/databases/$databaseId/resize"
                  params={{ databaseId: id }}
                  search={search}
                >
                  {votingDatabase(d.spec.engine) ? d.spec.mode === 'cluster' ? 'Change replicas' : 'Capacity' : ['clickhouse', 'oracle', 'vitess', 'duckdb'].includes(d.spec.engine) ? 'Capacity' : 'Resize'}
                </Link>
              </Button>
            )}
            {canManage && tab === 'backups' && (
              <Button asChild>
                <Link
                  to="/databases/$databaseId/recover"
                  params={{ databaseId: id }}
                  search={search}
                >
                  Recover
                </Link>
              </Button>
            )}
          </div>
        }
      />
      <div className="flex flex-wrap items-center justify-between gap-3 pb-4">
        <DatabaseIdentity database={d} />
        <div className="flex flex-wrap items-center gap-3">
          <span className="db-kicker">Observed {timestamp(d.observation.observed_at)}</span>
          <Button
            variant="ghost"
            size="sm"
            disabled={query.isFetching}
            onClick={() => {
              void query.refetch()
              if (tab === 'activity') void operations.refetch()
            }}
          >
            <Icon name="refresh" size={14} />
            Refresh
          </Button>
        </div>
      </div>
      {query.error && <Note>Refresh failed. Showing the last received observation.</Note>}
      {d.observation.message && <Note>{d.observation.message}</Note>}
      {databaseHealth(d, now) === 'Observation stale' && (
        <Note>
          This observation is stale. Member readiness and topology show the last known state.
        </Note>
      )}
      {error && (
        <p role="alert" className="text-destructive py-3">
          {error}
        </p>
      )}
      <Tabs.Root
        value={tab}
        onValueChange={(value) => {
          setError('')
          void navigate({
            to: '/databases/$databaseId',
            params: { databaseId: id },
            search: { ...search, tab: value as DatabaseTab },
          })
        }}
      >
        <Tabs.List ref={navigationRoot} className="tab-list" aria-label="Database sections">
          {databaseTabs.map((value) => (
            <Tabs.Trigger key={value} className="tab-trigger" value={value}>
              {
                {
                  overview: 'Overview',
                  monitoring: 'Monitoring',
                  connections: 'Connections & security',
                  backups: 'Backups',
                  activity: 'Activity',
                  settings: 'Settings',
                }[value]
              }
            </Tabs.Trigger>
          ))}
        </Tabs.List>
        <Tabs.Content value="overview" className="tab-content">
          <DatabaseSummary database={d} now={now} />
          <DatabaseTopology database={d} now={now} receivedAt={query.dataUpdatedAt} />
          <DatabaseMembers database={d} now={now} receivedAt={query.dataUpdatedAt} />
        </Tabs.Content>
        <Tabs.Content value="monitoring" className="tab-content">
          <DatabaseMonitoring
            key={`${d.id}-${d.revision}`}
            database={d}
            now={now}
            receivedAt={query.dataUpdatedAt}
          />
        </Tabs.Content>
        <Tabs.Content value="connections" className="tab-content">
          <div className="grid gap-4">
          <DatabaseConnections database={d} canManage={canManage} />
          <DatabaseSecurity database={d} now={now} />
          </div>
        </Tabs.Content>
        <Tabs.Content value="backups" className="tab-content">
          {d.recovery && (
            <div className="py-3">
              <Note>
                Recovery job{' '}
                <Link to="/backups/$jobId" params={{ jobId: d.recovery.job_id }}>
                  {d.recovery.job_id.slice(0, 8)}
                </Link>
                . Captured {timestamp(d.recovery.captured_at)}.{' '}
                {d.recovery.restored_at
                  ? 'Recovery completed. Inspect the data before changing an application connection.'
                  : 'Recovery has not been confirmed complete.'}{' '}
                Writes after the recovery point require a fresh capture before final cutover.
              </Note>
              {canManage && <DatabaseInspection database={d} />}
            </div>
          )}
          {canBackups ? (
            <DatabaseBackups database={d} />
          ) : (
            <section className="db-panel">
              <div className="db-panel-heading">
                <h2>Backups & recovery</h2>
              </div>
              <p className="db-inline-notice">Backup management requires backup access.</p>
            </section>
          )}
        </Tabs.Content>
        <Tabs.Content value="activity" className="tab-content">
          <section className="db-panel" aria-label="Operations">
            <div className="db-panel-heading">
              <h2>Recent operations</h2>
              <span className="db-kicker">
                {operations.isPending
                  ? 'Loading'
                  : operations.error
                    ? 'Unavailable'
                    : `${operations.data?.items.length ?? 0} recorded`}
              </span>
            </div>
            {operations.isPending ? (
              <Loading rows={2} />
            ) : operations.error ? (
              <ErrorState error={operations.error} />
            ) : !operations.data.items.length ? (
              <Empty
                title="No operations"
                description="No database operations have been recorded."
              />
            ) : (
              <ul>
                {operations.data.items.map((op) => (
                  <li key={op.id} className="db-operation">
                    <div className="db-operation-header">
                      <strong>{op.kind}</strong>
                      <Status value={op.status} />
                      <time dateTime={op.created_at}>{timestamp(op.created_at)}</time>
                    </div>
                    <div className="db-operation-meta">
                      <span>Revision {op.revision}</span>
                      <span>{op.phase}</span>
                      {op.finished_at && <span>Finished {timestamp(op.finished_at)}</span>}
                    </div>
                    {op.message && <p>{op.message}</p>}
                    {canManage && op.id === operations.data.items[0]?.id && votingDatabase(d.spec.engine) && d.spec.mode === 'cluster' && d.status === 'failed' && op.revision === d.revision && ['resize', 'resize-retry'].includes(op.kind) && op.status === 'failed' && <Button asChild><Link to="/databases/$databaseId/resize-retry" params={{ databaseId: id }} search={{ ...search, operation: op.id }}>Review retry</Link></Button>}
                    {canManage && op.kind === 'switchover' && op.status === 'failed' && op.switchover && op.phase !== 'switchover' && <Button asChild><Link to="/databases/$databaseId/switchover" params={{ databaseId: id }} search={{ ...search, operation: op.phase === 'review' ? undefined : op.id }}>{op.phase === 'review' ? 'Review current topology' : 'Review retry'}</Link></Button>}
                  </li>
                ))}
              </ul>
            )}
          </section>
        </Tabs.Content>
        <Tabs.Content value="settings" className="tab-content">
          {d.spec.engine === 'oracle' && d.spec.oracle?.edition === 'enterprise' && d.spec.mode === 'cluster' && <div className="flex flex-wrap gap-2 pb-4"><Button asChild disabled={!canManage || d.status !== 'ready'}><Link to="/databases/$databaseId/switchover" params={{ databaseId: id }} search={search}>Switch primary</Link></Button></div>}
          <section className="db-panel" aria-label="Database configuration">
            <div className="db-panel-heading">
              <h2>Configuration</h2>
              <span className="db-kicker">Revision {d.revision}</span>
            </div>
            <dl className="db-facts px-4 py-4">
              <div>
                <dt>Project</dt>
                <dd>{d.project}</dd>
              </div>
              <div>
                <dt>Environment</dt>
                <dd>{d.environment}</dd>
              </div>
              <div>
                <dt>Created</dt>
                <dd>{timestamp(d.created_at)}</dd>
              </div>
              <div>
                <dt>Updated</dt>
                <dd>{timestamp(d.updated_at)}</dd>
              </div>
              <div className="col-span-2">
                <dt>Database ID</dt>
                <dd className="flex items-center gap-2 font-mono text-xs">
                  {d.id}
                  <Copy value={d.id} />
                </dd>
              </div>
            </dl>
          </section>
          <div className="py-4">
            <Button disabled={!canManage} variant="danger" onClick={() => setDeleting((v) => !v)}>
              <Icon name="trash" size={14} />
              Delete database
            </Button>
          </div>
          {deleting && (
            <form
              className="grid gap-3 pb-4"
              onSubmit={async (e) => {
                e.preventDefault()
                setBusy(true)
                setError('')
                try {
                  await unwrap(
                    client.DELETE('/databases/{id}', {
                      params: { path: { id }, header: { 'Idempotency-Key': crypto.randomUUID() } },
                      body: { expected_revision: d.revision, confirm_name: confirmation },
                    }),
                  )
                  void navigate({ to: '/databases', search })
                } catch (err) {
                  setError(message(err))
                } finally {
                  setBusy(false)
                }
              }}
            >
              <Note>
                Deletion removes every database member and its data. Saved backups remain.
              </Note>
              <label>
                Type {d.spec.name} to confirm
                <Input value={confirmation} onChange={(e) => setConfirmation(e.target.value)} />
              </label>
              <div>
                <Button
                  type="submit"
                  variant="danger"
                  disabled={busy || confirmation !== d.spec.name}
                >
                  <Icon name="trash" size={14} />
                  Delete database and data
                </Button>
              </div>
            </form>
          )}
        </Tabs.Content>
      </Tabs.Root>
    </div>
  )
}

function DatabaseConnections({
  database: d,
  canManage,
}: {
  database: ManagedDatabase
  canManage: boolean
}) {
  const id = d.id
  const [credentials, setCredentials] = useState<{
    username: string
    password: string
    database: string
  } | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  return (
    <section className="db-panel" aria-label="Private endpoints">
      <div className="db-panel-heading">
        <h2>Connections</h2>
        <span className="db-kicker">Private network</span>
      </div>
      {!d.observation.endpoints?.length ? (
        <p className="db-inline-notice">No verified endpoints yet.</p>
      ) : (
        <dl>
          {d.observation.endpoints.map((e) => (
            <div key={e.purpose} className="db-connection">
              <dt>
                {endpointName(e.purpose, d.spec.engine)}
                <span className="text-muted-foreground">Port {e.port}</span>
              </dt>
              <dd>
                <code>
                  {endpointAddress(e, d.spec.engine)}
                </code>
                <Copy
                  iconOnly
                  value={endpointAddress(e, d.spec.engine)}
                  label={`Copy ${endpointName(e.purpose, d.spec.engine).toLowerCase()}`}
                />
              </dd>
            </div>
          ))}
        </dl>
      )}
      {d.spec.engine === 'redis' && d.spec.mode === 'cluster' && (
        <p className="db-inline-notice">
          Use a cluster-aware Redis client. The controller owns replication and failover.
        </p>
      )}
      {d.spec.engine === 'vitess' && <p className="db-inline-notice">Vitess uses the MySQL protocol on port 3306 through vtgate. {d.spec.replicas > 0 ? 'Use app@primary for writes or app@replica for replica reads.' : 'Use app@primary for reads and writes.'} Configure the driver with the mounted public CA and hostname verification.</p>}
      {d.spec.engine === 'mysql' && <p className="db-inline-notice">MySQL Router provides explicit write{d.spec.replicas > 0 ? ' and replica' : ''} routes. Clients must reconnect after failover. Configure the driver with the public CA and hostname verification.</p>}
      {d.spec.engine === 'mongodb' && <p className="db-inline-notice">Use a MongoDB driver with replica-set discovery, verified TLS and the public CA. The binding selects primary reads and majority acknowledgement. Retry failed transactions only when safe.</p>}
      {d.spec.pooling && <div className="px-4 py-3 grid gap-3">
        <h3 className="text-sm font-medium">PgBouncer</h3>
        <dl className="db-facts"><div><dt>Pooling mode</dt><dd>{d.spec.pooling.mode}</dd></div><div><dt>Instances per route</dt><dd>{d.spec.pooling.instances}</dd></div><div><dt>Clients per instance</dt><dd>{d.spec.pooling.max_client_connections}</dd></div><div><dt>Server connections per instance</dt><dd>{d.spec.pooling.default_pool_size}</dd></div></dl>
        <p className="text-xs text-muted-foreground">Write and replica routes are explicit. Existing sessions must reconnect after failover. Replica reads may lag.{d.spec.pooling.mode === 'transaction' ? ' Transaction pooling does not preserve session state between transactions.' : ' Session pooling retains the server connection for each client session.'}</p>
        {d.observation.pooling?.message && <p role="status" className="text-sm">{d.observation.pooling.message}</p>}
      </div>}
      <div className="px-4 py-3">
        <Button
          disabled={busy || !canManage}
          onClick={async () => {
            if (credentials) {
              setCredentials(null)
              return
            }
            setBusy(true)
            setError('')
            try {
              setCredentials(
                await unwrap(
                  client.POST('/databases/{id}/credentials', {
                    params: { path: { id } },
                    body: {},
                  }),
                ),
              )
            } catch (err) {
              setError(message(err))
            } finally {
              setBusy(false)
            }
          }}
        >
          {credentials ? 'Hide credentials' : 'Reveal credentials'}
        </Button>
        {error && (
          <p role="alert" className="text-destructive py-3">
            {error}
          </p>
        )}
        {credentials && (
          <dl className="grid gap-2 py-3">
            <div>
              <dt>Username</dt>
              <dd>{credentials.username}</dd>
            </div>
            <div>
              <dt>Database</dt>
              <dd>{credentials.database}</dd>
            </div>
            <div>
              <dt>Password</dt>
              <dd className="break-all font-mono select-all">{credentials.password}</dd>
            </div>
          </dl>
        )}
      </div>
    </section>
  )
}
