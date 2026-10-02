import { useScope, canAccess } from '../lib/scope'
import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { databaseSearch, databaseHealth, useDatabases } from '../lib/databases'
import { useExternalDatabases, externalDatabaseHealth } from '../lib/external-databases'
import { Empty, ErrorState, Loading, PageHeader } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Status } from '../components/shared'
import { ServiceIcon } from '../components/service-icon'
import { Icon } from '../components/icons'
import { SelectField } from '../components/ui/select'
import { engineName, expectedMembers, shardedDatabase } from '../lib/database-view'
import { timestamp } from '../lib/api'
import { projectRouteScopeMatches } from '../lib/projects'

export const Route = createFileRoute('/databases')({
  validateSearch: databaseSearch,
  component: DatabaseRoute,
})
function DatabaseRoute() {
  return useLocation().pathname === '/databases' ? <Databases /> : <Outlet />
}
function Databases() {
  const scope = Route.useSearch()
  const workspace = useScope()
  const scopeMatches = projectRouteScopeMatches(workspace, scope)
  const query = useDatabases(scope.project, scope.environment, scopeMatches)
  const external = useExternalDatabases(scope.project, scope.environment, scopeMatches)
  const canManage = scopeMatches && !workspace.identity.application && canAccess(workspace.identity, scope.project, 'deployments:write')
  const [filter, setFilter] = useState('')
  const [engine, setEngine] = useState('all')
  const filtered = (query.data?.items || []).filter(
    (d) =>
      d.spec.name.toLowerCase().includes(filter.toLowerCase()) &&
      (engine === 'all' || d.spec.engine === engine),
  )
  const externalFiltered = (external.data?.items || []).filter((d) =>
    d.spec.name.toLowerCase().includes(filter.toLowerCase()) &&
    (engine === 'all' || d.spec.engine === engine),
  )
  return (
    <div className="ops-page">
      <PageHeader
        title="Databases"
        description="Managed databases with independent resources, credentials, backups and lifecycle."
        action={
          canManage &&
          scope.project &&
          scope.environment && (
            <div className="flex flex-wrap gap-2">
              <Button asChild>
                <Link to="/databases/import" search={scope}>
                  Import backup
                </Link>
              </Button>
              <Button asChild variant="primary">
                <Link to="/databases/new" search={scope}>
                  New database
                </Link>
              </Button>
            </div>
          )
        }
      />
      {!scopeMatches ? (
        <Empty
          title="Workspace unavailable"
          description="Choose an accessible project and environment above."
        />
      ) : !scope.project || !scope.environment ? (
        <Empty
          title="Choose a project and environment"
          description="Select a project above, then open Databases to manage its resources."
        />
      ) : (
        <>
          <div className="resource-toolbar">
            <div className="w-full sm:w-72">
              <Input
                aria-label="Filter databases"
                placeholder="Find database…"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
              />
            </div>
            <SelectField
              label="Database engine"
              value={engine}
              onValueChange={setEngine}
              options={[
                { value: 'all', label: 'All engines' },
                { value: 'postgresql', label: 'PostgreSQL' },
                { value: 'redis', label: 'Redis' },
                { value: 'mysql', label: 'MySQL' },
                { value: 'mongodb', label: 'MongoDB' },
                { value: 'clickhouse', label: 'ClickHouse' },
                { value: 'oracle', label: 'Oracle Database' },
                { value: 'vitess', label: 'Vitess' },
              ]}
            />
            <Button onClick={() => { void query.refetch(); void external.refetch() }} disabled={query.isFetching || external.isFetching}>
              <Icon name="refresh" size={14} />
              Refresh
            </Button>
          </div>
          {query.isPending ? (
            <Loading />
          ) : query.error ? (
            <ErrorState error={query.error} />
          ) : !query.data.items.length && external.isSuccess && !external.data.items.length ? (
            <Empty
              title="No managed databases"
              description="Create a managed database with its own resources."
            />
          ) : !filtered.length && external.isSuccess && !externalFiltered.length ? (
            <Empty title="No matching databases" description="Try a different database name." />
          ) : filtered.length > 0 ? (
            <>
              <div className="flex flex-wrap gap-x-5 gap-y-2 pb-4 text-xs text-muted-foreground">
                <span>{filtered.length} managed databases</span>
                <span>{filtered.filter((d) => databaseHealth(d) === 'ready').length} healthy</span>
                <span>
                  {filtered.filter((d) => databaseHealth(d) !== 'ready').length} need attention
                </span>
                <span>
                  {filtered.reduce((n, d) => n + expectedMembers(d.spec), 0)} configured members
                </span>
              </div>
              <div className="db-catalog">
                {filtered.map((d) => (
                  <article key={d.id} className="db-catalog-card" data-engine={d.spec.engine}>
                    <div className="db-catalog-card-heading">
                      <ServiceIcon name={d.spec.engine} size={36} />
                      <div>
                        <h2>
                          <Link
                            to="/databases/$databaseId"
                            params={{ databaseId: d.id }}
                            search={{ project: d.project, environment: d.environment }}
                          >
                            {d.spec.name}
                          </Link>
                        </h2>
                        <p>
                          {engineName(d.spec.engine)} {d.spec.version} ·{' '}
                          {d.spec.mode === 'standalone' ? 'Standalone' : 'Cluster'}
                        </p>
                      </div>
                      <Status
                        value={
                          d.status === 'ready'
                            ? databaseHealth(d) === 'Observation stale'
                              ? 'stale'
                              : databaseHealth(d)
                            : d.status
                        }
                      />
                    </div>
                    <dl className="db-catalog-facts">
                      <div>
                        <dt>CPU / member</dt>
                        <dd>{d.spec.cpu}</dd>
                      </div>
                      <div>
                        <dt>Memory</dt>
                        <dd>{d.spec.memory}</dd>
                      </div>
                      <div>
                        <dt>Disk / member</dt>
                        <dd>{d.spec.storage_gib} GiB{d.spec.engine === 'mongodb' ? ' data + 1 GiB logs' : ''}</dd>
                      </div>
                    </dl>
                    <div className="db-catalog-member-strip">
                      <Icon name="network" size={14} />
                      <span>
                        {databaseHealth(d) === 'Not observed'
                          ? 'Readiness not observed'
                          : `${d.observation.members?.filter((m) => m.ready).length || 0} / ${expectedMembers(d.spec)} ready${databaseHealth(d) === 'Observation stale' ? ' · last known' : ''}`}
                      </span>
                      <span className="ml-auto flex gap-1" aria-hidden="true">
                        {(d.observation.members || []).slice(0, 12).map((m) => (
                          <i key={m.uid} data-ready={m.ready && databaseHealth(d) === 'ready'} />
                        ))}
                      </span>
                    </div>
                    <div className="db-catalog-footer">
                      <span>
                        {shardedDatabase(d.spec.engine) && d.spec.mode === 'cluster'
                          ? `${d.spec.shards} shards · `
                          : ''}
                        {d.spec.replicas} {d.spec.replicas === 1 ? 'replica' : 'replicas'}
                        {shardedDatabase(d.spec.engine) && d.spec.mode === 'cluster' ? ' / shard' : ''}
                      </span>
                      <span className="ml-auto">r{d.revision}</span>
                    </div>
                    {d.observation.message && (
                      <p className="text-xs text-muted-foreground" role="status">
                        {d.observation.message}
                      </p>
                    )}
                    <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border pt-3">
                      <span className="text-xs text-muted-foreground">
                        Observed{' '}
                        {d.observation.observed_at?.startsWith('0001-')
                          ? 'not yet'
                          : timestamp(d.observation.observed_at)}
                      </span>
                      <Button asChild variant="ghost" size="sm">
                        <Link
                          to="/databases/$databaseId"
                          params={{ databaseId: d.id }}
                          search={{ project: d.project, environment: d.environment }}
                        >
                          Inspect
                          <Icon name="arrow" size={14} />
                        </Link>
                      </Button>
                    </div>
                  </article>
                ))}
              </div>
            </>
          ) : null}
          {external.error && <ErrorState error={external.error} />}
          {external.isPending && <Loading />}
          {externalFiltered.length > 0 && <section className="grid gap-3 pt-5" aria-labelledby="external-databases-heading">
            <h2 id="external-databases-heading" className="text-sm font-semibold">Legacy provider connections <span className="font-normal text-muted-foreground">· {externalFiltered.length}</span></h2>
            <div className="db-catalog">{externalFiltered.map((d) => <article key={d.id} className="db-catalog-card">
              <div className="db-catalog-card-heading"><ServiceIcon name={d.spec.engine} size={36} /><div><h2><Link to="/databases/external/$externalDatabaseId" params={{ externalDatabaseId: d.id }} search={{ project: d.project, environment: d.environment }}>{d.spec.name}</Link></h2><p>Legacy external connection · {engineName(d.spec.engine)}</p></div><Status value={externalDatabaseHealth(d) === 'Observation stale' ? 'stale' : externalDatabaseHealth(d)} /></div>
              <dl className="db-catalog-facts"><div><dt>Database</dt><dd>{d.spec.database}</dd></div><div><dt>Port</dt><dd>{d.spec.port}</dd></div><div><dt>Credentials</dt><dd>r{d.credential_revision}</dd></div></dl>
              <p className="db-catalog-host">{d.spec.host}</p>
              <div className="db-catalog-footer"><span>Inspection and removal only</span><span className="ml-auto">r{d.revision}</span></div>
              <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border pt-3"><span className="text-xs text-muted-foreground">Checked {d.observation.revision ? timestamp(d.observation.observed_at) : 'not yet'}</span><Button asChild variant="ghost" size="sm"><Link to="/databases/external/$externalDatabaseId" params={{ externalDatabaseId: d.id }} search={{ project: d.project, environment: d.environment }}>Inspect<Icon name="arrow" size={14} /></Link></Button></div>
            </article>)}</div>
          </section>}
        </>
      )}
    </div>
  )
}
