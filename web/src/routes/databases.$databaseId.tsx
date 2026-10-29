import { DatabaseInspection } from '../components/database-inspection'
import { useEffect, useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { useActiveSection } from '../lib/use-active-section'
import { createFileRoute, Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { client, unwrap } from '../lib/client'
import { useDatabase, type ManagedDatabase, useDatabaseOperations } from '../lib/databases'
import { useResourceScope, useScope, canAccess } from '../lib/scope'
import { message, relative, timestamp } from '../lib/api'
import {
  ErrorState,
  HeadingHelp,
  Loading,
  RequestError,
  Status,
} from '../components/shared'
import { Button } from '../components/ui/button'
import { Icon } from '../components/icons'
import { ServiceIcon } from '../components/service-icon'
import { ResourceEmpty, ResourceError, ResourceRow, ResourceRows } from '../components/resource-card'
import { CopyValue } from '../components/operation-review'
import { DatabaseMonitoring, DatabaseMembers, DatabaseTopology } from '../components/database-cockpit'
import {
  DatabaseActivity,
  DatabaseAttention,
  DatabaseDetails,
  DatabaseEndpoints,
  DatabaseHealthLine,
  DatabaseOperations,
} from '../components/database-workspace'
import { DatabaseBackups } from '../components/database-backups'
import { DatabaseSecurity } from '../components/database-security'
import { databaseLayoutSummary, engineName, expectedMembers } from '../lib/database-view'

const databaseTabs = [
  'overview',
  'topology',
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
  const operations = useDatabaseOperations(
    id,
    ['overview', 'activity'].includes(tab) && Boolean(query.data),
  )
  const navigate = useNavigate()
  const { identity } = useScope()
  if (query.isPending) return <Loading />
  if (query.error && !query.data) return <ErrorState error={query.error} />
  const d = query.data!
  const canManage = !identity.application && canAccess(identity, d.project, 'deployments:write')
  const canQuery =
    !identity.application &&
    ['databases:query', 'databases:write-query'].some(
      (permission) =>
        canAccess(identity, d.project, permission) &&
        (identity.credential_type !== 'machine' ||
          (identity.project === d.project &&
            identity.environment === d.environment &&
            identity.permissions.includes(permission))),
    )
  const search = { project: d.project, environment: d.environment }
  const canBackups = identity.admin || identity.can_manage_backups
  const history = [...(operations.data?.items || [])].sort(
    (a, b) => Date.parse(b.created_at) - Date.parse(a.created_at),
  )
  const openTab = (value: DatabaseTab) =>
    void navigate({
      to: '/databases/$databaseId',
      params: { databaseId: id },
      search: { ...search, tab: value },
    })
  const refresh = () => {
    void query.refetch()
    void operations.refetch()
  }
  return (
    <div className="ops-page app-workspace db-workspace">
      <header className="app-workspace-head">
        <div className="app-workspace-identity">
          <nav className="app-workspace-scope" aria-label="Database scope">
            <Link to="/projects/$project" params={{ project: d.project }}>
              {d.project}
            </Link>
            <span aria-hidden="true">/</span>
            <span>{d.environment}</span>
            <span aria-hidden="true">/</span>
            <Link to="/databases" search={search}>
              Databases
            </Link>
          </nav>
          <div className="app-workspace-title">
            <ServiceIcon name={d.spec.engine} size={24} />
            <h1>{d.spec.name}</h1>
          </div>
          <div className="app-workspace-meta">
            <DatabaseHealthLine database={d} now={now} />
            <span>
              {engineName(d.spec.engine)} {d.spec.version} · {databaseLayoutSummary(d.spec)}
            </span>
            <span>Revision {d.revision}</span>
            <span>Updated {relative(d.updated_at).toLowerCase()}</span>
          </div>
        </div>
        <div className="app-workspace-actions">
          {canQuery && d.spec.engine === 'postgresql' && d.status === 'ready' && (
            <Button asChild>
              <Link to="/databases/$databaseId/query" params={{ databaseId: id }} search={search}>
                <Icon name="code" size={16} />
                SQL query
              </Link>
            </Button>
          )}
          {canManage && d.status === 'ready' && (
            <Button asChild variant="primary">
              <Link to="/databases/$databaseId/connect" params={{ databaseId: id }} search={search}>
                Connect application
              </Link>
            </Button>
          )}
          <Menu
            trigger={
              <Button variant="ghost" size="icon" aria-label="More database actions">
                <span aria-hidden="true">···</span>
              </Button>
            }
          >
            <MenuItem onSelect={() => void navigator.clipboard?.writeText(d.id)}>
              <Icon name="copy" size={14} />
              Copy database ID
            </MenuItem>
            <MenuItem onSelect={refresh}>
              <Icon name="refresh" size={14} />
              Refresh now
            </MenuItem>
            <MenuItem
              onSelect={() =>
                void navigate({
                  to: '/databases/$databaseId/private-access',
                  params: { databaseId: id },
                  search,
                })
              }
            >
              <Icon name="terminal" size={14} />
              Private connection
            </MenuItem>
            <MenuItem onSelect={() => openTab('settings')}>
              <Icon name="settings" size={14} />
              All operations
            </MenuItem>
          </Menu>
        </div>
      </header>
      <Tabs.Root value={tab} onValueChange={(value) => openTab(value as DatabaseTab)}>
        <Tabs.List ref={navigationRoot} className="tab-list" aria-label="Database sections">
          {databaseTabs.map((value) => (
            <Tabs.Trigger key={value} className="tab-trigger" value={value}>
              {
                {
                  overview: 'Overview',
                  topology: 'Topology',
                  monitoring: 'Monitoring',
                  connections: 'Connections',
                  backups: 'Backups',
                  activity: 'Activity',
                  settings: 'Operations',
                }[value]
              }
            </Tabs.Trigger>
          ))}
        </Tabs.List>
        <Tabs.Content value="overview" className="tab-content app-workspace-overview">
          <DatabaseAttention
            database={d}
            now={now}
            refreshFailed={Boolean(query.error)}
            latest={history[0]}
            canManage={canManage}
            onRefresh={refresh}
            onTab={openTab}
          />
          <div className="app-workspace-columns">
            <div className="app-workspace-overview">
              <section className="app-workspace-section" aria-labelledby="db-endpoints-heading">
                <div className="app-workspace-section-head">
                  <div className="db-section-title">
                    <h2 id="db-endpoints-heading">Connect</h2>
                    <DriverHelp database={d} />
                  </div>
                  <Button size="sm" variant="ghost" className="app-workspace-link" onClick={() => openTab('connections')}>
                    Credentials and TLS
                  </Button>
                </div>
                <DatabaseEndpoints database={d} />
              </section>
              <section className="app-workspace-section" aria-labelledby="db-members-heading">
                <div className="app-workspace-section-head">
                  <h2 id="db-members-heading">Members</h2>
                  <span className="app-workspace-count">
                    {d.observation.members?.length || 0} observed · {expectedMembers(d.spec)} desired
                  </span>
                </div>
                <DatabaseMembers database={d} now={now} receivedAt={query.dataUpdatedAt} />
              </section>
            </div>
            <div className="app-workspace-overview">
              <section className="app-workspace-section" aria-labelledby="db-details-heading">
                <div className="app-workspace-section-head">
                  <h2 id="db-details-heading">Details</h2>
                </div>
                <DatabaseDetails database={d} />
              </section>
              <section className="app-workspace-section" aria-labelledby="db-recent-heading">
                <div className="app-workspace-section-head">
                  <h2 id="db-recent-heading">Recent operations</h2>
                  {history.length > 3 && (
                    <Button size="sm" variant="ghost" className="app-workspace-link" onClick={() => openTab('activity')}>
                      View all
                    </Button>
                  )}
                </div>
                {operations.isPending ? (
                  <Loading rows={2} />
                ) : operations.error ? (
                  <ResourceError error={operations.error} retry={() => void operations.refetch()} subject="database operations" />
                ) : (
                  <DatabaseActivity database={d} operations={history} limit={3} />
                )}
              </section>
            </div>
          </div>
        </Tabs.Content>
        <Tabs.Content value="topology" className="tab-content app-workspace-overview">
          {d.observation.members?.length || d.observation.endpoints?.length ? (
            <DatabaseTopology database={d} now={now} receivedAt={query.dataUpdatedAt} />
          ) : (
            <ResourceEmpty
              icon="network"
              title="No topology observed"
              description={`Members, endpoints and application bindings appear once ${d.spec.name} is observed.`}
            />
          )}
        </Tabs.Content>
        <Tabs.Content value="monitoring" className="tab-content">
          <DatabaseMonitoring
            key={`${d.id}-${d.revision}`}
            database={d}
            now={now}
            receivedAt={query.dataUpdatedAt}
          />
        </Tabs.Content>
        <Tabs.Content value="connections" className="tab-content app-workspace-overview">
          <DatabaseConnections database={d} canManage={canManage} />
          <DatabaseSecurity database={d} now={now} />
        </Tabs.Content>
        <Tabs.Content value="backups" className="tab-content app-workspace-overview">
          {d.recovery && (
            <section className="app-workspace-section" aria-labelledby="db-recovery-heading">
              <div className="app-workspace-section-head">
                <h2 id="db-recovery-heading">Recovery</h2>
                <Status value={d.recovery.restored_at ? 'completed' : 'pending'} small />
              </div>
              <dl className="app-workspace-details">
                <div>
                  <dt>Recovery job</dt>
                  <dd>
                    <Link to="/backups/$jobId" params={{ jobId: d.recovery.job_id }}>
                      {d.recovery.job_id.slice(0, 8)}
                    </Link>
                  </dd>
                </div>
                <div>
                  <dt>Recovery point</dt>
                  <dd>{d.recovery.captured_at ? timestamp(d.recovery.captured_at) : 'Not recorded'}</dd>
                </div>
                <div>
                  <dt>Restored</dt>
                  <dd>{d.recovery.restored_at ? timestamp(d.recovery.restored_at) : 'Not confirmed'}</dd>
                </div>
                <div>
                  <dt>Inspection</dt>
                  <dd>{d.recovery.inspected_at ? timestamp(d.recovery.inspected_at) : 'Not recorded'}</dd>
                </div>
              </dl>
              <p className="db-section-note">
                Writes after the recovery point need a fresh capture before final cutover.
              </p>
              {canManage && !d.recovery.inspected_at && <DatabaseInspection database={d} />}
            </section>
          )}
          {canBackups ? (
            <DatabaseBackups database={d} />
          ) : (
            <ResourceEmpty
              icon="lock"
              title="Backup management requires backup access"
              description="Ask an administrator for backup access to see archives and schedules."
            />
          )}
        </Tabs.Content>
        <Tabs.Content value="activity" className="tab-content app-workspace-overview">
          <section className="app-workspace-section" aria-labelledby="db-activity-heading">
            <div className="app-workspace-section-head">
              <h2 id="db-activity-heading">Operations</h2>
              <span className="app-workspace-count">
                {operations.isPending
                  ? 'Loading'
                  : operations.error
                    ? 'Unavailable'
                    : `${history.length} recorded`}
              </span>
            </div>
            {operations.isPending ? (
              <Loading rows={2} />
            ) : operations.error ? (
              <ResourceError error={operations.error} retry={() => void operations.refetch()} subject="database operations" />
            ) : (
              <DatabaseActivity database={d} operations={history} />
            )}
          </section>
        </Tabs.Content>
        <Tabs.Content value="settings" className="tab-content app-workspace-overview">
          <section className="app-workspace-section" aria-labelledby="db-operations-heading">
            <div className="app-workspace-section-head">
              <h2 id="db-operations-heading">Operations</h2>
              <HeadingHelp title="database operations">
                Each operation opens its own review that shows scope, target and impact before
                anything changes.
              </HeadingHelp>
            </div>
            <DatabaseOperations database={d} canManage={canManage} canQuery={canQuery} />
          </section>
        </Tabs.Content>
      </Tabs.Root>
    </div>
  )
}

/** Engine-specific driver settings, kept in help next to the endpoints they apply to. */
function driverGuidance(d: ManagedDatabase) {
  const replicas = d.spec.replicas > 0
  switch (d.spec.engine) {
    case 'redis':
      return d.spec.mode === 'cluster'
        ? 'Use a cluster-aware Redis client. The controller owns replication and failover.'
        : 'Connect with a standard Redis client using the endpoint and credentials.'
    case 'vitess':
      return `Vitess uses the MySQL protocol on port 3306 through vtgate. ${replicas ? 'Use app@primary for writes or app@replica for replica reads.' : 'Use app@primary for reads and writes.'} Configure the driver with the mounted public CA and hostname verification.`
    case 'mysql':
      return `MySQL Router provides explicit write${replicas ? ' and replica' : ''} routes. Clients must reconnect after failover. Configure the driver with the public CA and hostname verification.`
    case 'mongodb':
      return 'Use a MongoDB driver with replica-set discovery, verified TLS and the public CA. The binding selects primary reads and majority acknowledgement. Retry failed transactions only when safe.'
    default:
      return 'Application bindings receive these endpoints privately. External clients need a private connection and the public CA.'
  }
}
function DriverHelp({ database: d }: { database: ManagedDatabase }) {
  return <HeadingHelp title="connecting clients">{driverGuidance(d)}</HeadingHelp>
}

function DatabaseConnections({
  database: d,
  canManage,
}: {
  database: ManagedDatabase
  canManage: boolean
}) {
  const [credentials, setCredentials] = useState<{
    username: string
    password: string
    database: string
  } | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const search = { project: d.project, environment: d.environment }
  return (
    <>
      <section className="app-workspace-section" aria-labelledby="db-private-heading">
        <div className="app-workspace-section-head">
          <div className="db-section-title">
            <h2 id="db-private-heading">Private endpoints</h2>
            <DriverHelp database={d} />
          </div>
          <Button asChild size="sm">
            <Link to="/databases/$databaseId/private-access" params={{ databaseId: d.id }} search={search}>
              Private connection
            </Link>
          </Button>
        </div>
        <DatabaseEndpoints database={d} />
        {d.spec.engine === 'redis' && d.spec.mode === 'cluster' && (
          <p className="db-section-note">Clients must be cluster-aware.</p>
        )}
      </section>
      <section className="app-workspace-section" aria-labelledby="db-credentials-heading">
        <div className="app-workspace-section-head">
          <div className="db-section-title">
            <h2 id="db-credentials-heading">Credentials</h2>
            <HeadingHelp title="database credentials">
              Credentials are read on request and are not stored in the page. Application bindings
              receive their own values; you do not need to copy these into an application.
            </HeadingHelp>
          </div>
          <Button
            size="sm"
            disabled={busy || !canManage}
            aria-busy={busy || undefined}
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
                      params: { path: { id: d.id } },
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
            <Icon name="key" size={14} />
            {busy ? 'Revealing…' : credentials ? 'Hide credentials' : 'Reveal credentials'}
          </Button>
        </div>
        {!canManage && <p className="db-section-note">Revealing credentials requires deployment access.</p>}
        {error && <RequestError error={error} />}
        {credentials && (
          <ResourceRows label="Database credentials" className="db-endpoint-rows">
            {(
              [
                ['Username', credentials.username],
                ['Database', credentials.database],
                ['Password', credentials.password],
              ] as const
            ).map(([label, value]) => (
              <ResourceRow
                key={label}
                identity={<span>{label}</span>}
                detail={<code className="select-all">{value}</code>}
                actions={<CopyValue value={value} what={label.toLowerCase()} />}
              />
            ))}
          </ResourceRows>
        )}
      </section>
      {d.spec.pooling && (
        <section className="app-workspace-section" aria-labelledby="db-pooling-heading">
          <div className="app-workspace-section-head">
            <h2 id="db-pooling-heading">PgBouncer pooling</h2>
            <HeadingHelp title="connection pooling">
              Write and replica routes are explicit. Existing sessions must reconnect after
              failover. Replica reads may lag.
              {d.spec.pooling.mode === 'transaction'
                ? ' Transaction pooling does not preserve session state between transactions.'
                : ' Session pooling retains the server connection for each client session.'}
            </HeadingHelp>
          </div>
          <dl className="app-workspace-details">
            <div>
              <dt>Pooling mode</dt>
              <dd>{d.spec.pooling.mode}</dd>
            </div>
            <div>
              <dt>Instances per route</dt>
              <dd>{d.spec.pooling.instances}</dd>
            </div>
            <div>
              <dt>Clients per instance</dt>
              <dd>{d.spec.pooling.max_client_connections}</dd>
            </div>
            <div>
              <dt>Server connections per instance</dt>
              <dd>{d.spec.pooling.default_pool_size}</dd>
            </div>
          </dl>
          {d.observation.pooling?.message && (
            <p role="status" className="db-section-note">
              {d.observation.pooling.message}
            </p>
          )}
        </section>
      )}
    </>
  )
}
