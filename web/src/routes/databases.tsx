import { DatabaseExplorerLink } from '../components/database-explorer-link'
import { useScope, canAccess } from '../lib/scope'
import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { databaseState, readyMembers } from '../components/database-workspace'
import { databaseSearch, useDatabases } from '../lib/databases'
import { useExternalDatabases, externalDatabaseHealth } from '../lib/external-databases'
import { Copy, Loading, PageHeader } from '../components/shared'
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
import { databaseLayoutSummary, endpointAddress, engineName, expectedMembers } from '../lib/database-view'
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
  // Databases that need attention come first; the API order is kept within each group.
  const ranked = [...filtered].sort(
    (a, b) => Number(databaseState(a) === 'ready') - Number(databaseState(b) === 'ready'),
  )
  const navigate = useNavigate()
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
        <ResourceEmpty
          icon="lock"
          title="Workspace unavailable"
          description="Choose an accessible project and environment above."
        />
      ) : !scope.project || !scope.environment ? (
        <ResourceEmpty
          icon="database"
          title="Choose a project and environment"
          description="Databases belong to one project environment."
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
              <div className="db-list-summary" role="status">
                <span>{filtered.length} managed databases</span>
                <span>{filtered.filter((d) => databaseState(d) === 'ready').length} ready</span>
                <span>
                  {filtered.filter((d) => databaseState(d) !== 'ready').length} need attention
                </span>
                <span>
                  {filtered.reduce((n, d) => n + expectedMembers(d.spec), 0)} configured members
                </span>
              </div>
              <div className="resource-grid" aria-label="Managed databases">
                {ranked.map((d) => {
                  const state = databaseState(d)
                  const endpoint = d.observation.endpoints?.[0]
                  const address = endpoint ? endpointAddress(endpoint, d.spec.engine) : ''
                  const search = { project: d.project, environment: d.environment }
                  const open = (tab?: 'connections' | 'backups' | 'settings') =>
                    void navigate({
                      to: '/databases/$databaseId',
                      params: { databaseId: d.id },
                      search: { ...search, tab },
                    })
                  return (
                    <ResourceCard
                      key={d.id}
                      cue={<ServiceIcon name={d.spec.engine} size={20} />}
                      name={
                        <Link
                          className="ops-card-link"
                          to="/databases/$databaseId"
                          params={{ databaseId: d.id }}
                          search={search}
                        >
                          {d.spec.name}
                        </Link>
                      }
                      menu={
                        <Menu
                          trigger={
                            <Button variant="ghost" size="icon" aria-label={`Actions for ${d.spec.name}`}>
                              <span aria-hidden="true">···</span>
                            </Button>
                          }
                        >
                          {canManage && d.status === 'ready' && (
                            <MenuItem
                              onSelect={() =>
                                void navigate({
                                  to: '/databases/$databaseId/connect',
                                  params: { databaseId: d.id },
                                  search,
                                })
                              }
                            >
                              <Icon name="plus" size={14} />
                              Connect application
                            </MenuItem>
                          )}
                          <MenuItem onSelect={() => open('connections')}>
                            <Icon name="key" size={14} />
                            Connections and credentials
                          </MenuItem>
                          <MenuItem onSelect={() => open('backups')}>
                            <Icon name="archive" size={14} />
                            Backups
                          </MenuItem>
                          <MenuItem onSelect={() => open('settings')}>
                            <Icon name="settings" size={14} />
                            Operations
                          </MenuItem>
                        </Menu>
                      }
                      meta={
                        <>
                          {engineName(d.spec.engine)} {d.spec.version} · {databaseLayoutSummary(d.spec)} ·{' '}
                          {d.spec.cpu} CPU · {d.spec.memory} · {d.spec.storage_gib} GiB per member
                        </>
                      }
                      endpoint={
                        address ? (
                          <>
                            <code title={address}>{address}</code>
                            <Copy value={address} label={`Copy ${d.spec.name} endpoint`} iconOnly />
                          </>
                        ) : (
                          <span className="resource-card-absent">
                            {d.status === 'ready' ? 'No verified endpoint' : 'No endpoint yet'}
                          </span>
                        )
                      }
                      state={
                        <ObservedState
                          status={state}
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
                          window={state === 'stale' ? 'last known' : 'now'}
                          value={readyMembers(d)}
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
