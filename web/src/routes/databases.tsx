import { DatabaseExplorerLink } from '../components/database-explorer-link'
import { useScope, canAccess } from '../lib/scope'
import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { databaseSearch, databaseHealth, useDatabases } from '../lib/databases'
import { useExternalDatabases, externalDatabaseHealth } from '../lib/external-databases'
import { Empty, Loading, PageHeader } from '../components/shared'
import {
  CardMetric,
  ObservedState,
  ResourceCard,
  ResourceEmpty,
  ResourceError,
  StaleNotice,
} from '../components/resource-card'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { ServiceIcon } from '../components/service-icon'
import { Icon } from '../components/icons'
import { SelectField } from '../components/ui/select'
import { engineName, expectedMembers, shardedDatabase } from '../lib/database-view'
import { projectRouteScopeMatches } from '../lib/projects'
import { DatabaseResourceNavigation } from '../components/database-resource-navigation'
import { databaseEngines } from '../lib/database-create'

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
  const canManage =
    scopeMatches &&
    !workspace.identity.application &&
    canAccess(workspace.identity, scope.project, 'deployments:write')
  const [filter, setFilter] = useState('')
  const [engine, setEngine] = useState('all')
  const filtered = (query.data?.items || []).filter(
    (d) =>
      d.spec.name.toLowerCase().includes(filter.toLowerCase()) &&
      (engine === 'all' || d.spec.engine === engine),
  )
  const externalFiltered = (external.data?.items || []).filter(
    (d) =>
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
      <DatabaseResourceNavigation active="databases" scope={scope} />
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
            <DatabaseExplorerLink project={scope.project} environment={scope.environment} />
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
                ...databaseEngines.map(({ id, name }) => ({ value: id, label: name })),
              ]}
            />
            <Button
              onClick={() => {
                void query.refetch()
                void external.refetch()
              }}
              disabled={query.isFetching || external.isFetching}
            >
              <Icon name="refresh" size={14} />
              Refresh
            </Button>
          </div>
          {query.isPending ? (
            <Loading />
          ) : query.error && !query.data ? (
            <ResourceError
              error={query.error}
              retry={() => void query.refetch()}
              subject="databases in this environment"
            />
          ) : !query.data.items.length && external.isSuccess && !external.data.items.length ? (
            <ResourceEmpty
              icon="database"
              title="No managed databases"
              description="Managed databases run with their own resources, credentials and backups."
              action={
                canManage && (
                  <Button asChild>
                    <Link to="/databases/new" search={scope}>
                      New database
                    </Link>
                  </Button>
                )
              }
            />
          ) : !filtered.length && external.isSuccess && !externalFiltered.length ? (
            <ResourceEmpty
              icon="search"
              title="No matching databases"
              description="Try a different database name or engine."
            />
          ) : filtered.length > 0 ? (
            <>
              {query.error && (
                <StaleNotice updatedAt={query.dataUpdatedAt} retry={() => void query.refetch()} />
              )}
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
              <div className="resource-grid" aria-label="Managed databases">
                {filtered.map((d) => {
                  const health = databaseHealth(d)
                  const sharded = shardedDatabase(d.spec.engine) && d.spec.mode === 'cluster'
                  return (
                    <ResourceCard
                      key={d.id}
                      cue={<ServiceIcon name={d.spec.engine} size={20} />}
                      name={
                        <Link
                          className="ops-card-link"
                          to="/databases/$databaseId"
                          params={{ databaseId: d.id }}
                          search={{ project: d.project, environment: d.environment }}
                        >
                          {d.spec.name}
                        </Link>
                      }
                      meta={
                        <>
                          {engineName(d.spec.engine)} {d.spec.version} ·{' '}
                          {d.spec.mode === 'standalone' ? 'Standalone' : 'Cluster'} · {d.spec.cpu}{' '}
                          CPU / {d.spec.memory} · {d.spec.storage_gib} GiB
                          {d.spec.engine === 'mongodb' ? ' data + 1 GiB logs' : ''} per member · r
                          {d.revision}
                        </>
                      }
                      endpoint={
                        <span>
                          {sharded ? `${d.spec.shards} shards · ` : ''}
                          {d.spec.replicas} {d.spec.replicas === 1 ? 'replica' : 'replicas'}
                          {sharded ? ' per shard' : ''}
                        </span>
                      }
                      state={
                        <ObservedState
                          status={
                            d.status === 'ready'
                              ? health === 'Observation stale'
                                ? 'stale'
                                : health
                              : d.status
                          }
                          observedAt={
                            d.observation.observed_at?.startsWith('0001-')
                              ? undefined
                              : d.observation.observed_at
                          }
                          note={d.observation.message}
                        />
                      }
                      metric={
                        <CardMetric
                          label="Ready members"
                          window="now"
                          value={
                            health === 'Not observed' || health === 'Observation stale'
                              ? undefined
                              : `${d.observation.members?.filter((m) => m.ready).length || 0} / ${expectedMembers(d.spec)}`
                          }
                        />
                      }
                    />
                  )
                })}
              </div>
            </>
          ) : null}
          {external.error && !external.data && (
            <ResourceError
              error={external.error}
              retry={() => void external.refetch()}
              subject="legacy provider connections"
            />
          )}
          {external.isPending && <Loading />}
          {externalFiltered.length > 0 && (
            <section className="grid gap-3 pt-5" aria-labelledby="external-databases-heading">
              <h2 id="external-databases-heading" className="text-sm font-semibold">
                Legacy provider connections{' '}
                <span className="font-normal text-muted-foreground">
                  · {externalFiltered.length}
                </span>
              </h2>
              <div className="resource-grid">
                {externalFiltered.map((d) => (
                  <ResourceCard
                    key={d.id}
                    cue={<ServiceIcon name={d.spec.engine} size={20} />}
                    name={
                      <Link
                        className="ops-card-link"
                        to="/databases/external/$externalDatabaseId"
                        params={{ externalDatabaseId: d.id }}
                        search={{ project: d.project, environment: d.environment }}
                      >
                        {d.spec.name}
                      </Link>
                    }
                    meta={
                      <>
                        Legacy external connection · {engineName(d.spec.engine)} · credentials r
                        {d.credential_revision} · inspection and removal only
                      </>
                    }
                    endpoint={
                      <code title={`${d.spec.host}:${d.spec.port}/${d.spec.database}`}>
                        {d.spec.host}:{d.spec.port}/{d.spec.database}
                      </code>
                    }
                    state={
                      <ObservedState
                        status={
                          externalDatabaseHealth(d) === 'Observation stale'
                            ? 'stale'
                            : externalDatabaseHealth(d)
                        }
                        observedAt={d.observation.revision ? d.observation.observed_at : undefined}
                      />
                    }
                  />
                ))}
              </div>
            </section>
          )}
        </>
      )}
    </div>
  )
}
