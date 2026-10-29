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
  GuidedEmpty,
  ResourceEmpty,
  ResourceError,
  StaleNotice,
} from '../components/resource-card'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { ServiceIcon } from '../components/service-icon'
import { Icon } from '../components/icons'
import { SelectField } from '../components/ui/select'
import { databaseLayoutSummary, endpointAddress, engineName } from '../lib/database-view'
import { projectRouteScopeMatches } from '../lib/projects'
import { DatabaseResourceNavigation } from '../components/database-resource-navigation'
import { databaseEngines } from '../lib/database-create'

const pendingStates = ['pending', 'provisioning', 'creating', 'not observed']
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
  // Failed first, then stale, then provisioning, then ready; API order is kept within each group.
  const rank = (state: string) =>
    state === 'ready' ? 3 : state === 'stale' ? 1 : pendingStates.includes(state) ? 2 : 0
  const ranked = [...filtered].sort((a, b) => rank(databaseState(a)) - rank(databaseState(b)))
  const counts = filtered.reduce<Record<string, number>>((all, d) => {
    const state = databaseState(d)
    const group =
      state === 'ready'
        ? 'ready'
        : state === 'stale'
          ? 'stale'
          : pendingStates.includes(state)
            ? 'provisioning'
            : 'failing'
    all[group] = (all[group] || 0) + 1
    return all
  }, {})
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
          scope.environment &&
          !(
            query.data &&
            !query.data.items.length &&
            external.isSuccess &&
            !external.data.items.length
          ) && (
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
          {!(
            query.data &&
            !query.data.items.length &&
            external.isSuccess &&
            !external.data.items.length
          ) && (
            <div className="resource-toolbar db-list-toolbar">
              <DatabaseExplorerLink project={scope.project} environment={scope.environment} />
              <div className="ops-search-input">
                <Icon name="search" size={15} />
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
          )}
          {query.isPending ? (
            <Loading />
          ) : query.error && !query.data ? (
            <ResourceError
              error={query.error}
              retry={() => void query.refetch()}
              subject="databases in this environment"
            />
          ) : !query.data.items.length &&
            external.isSuccess &&
            !external.data.items.length &&
            canManage ? (
            <GuidedEmpty
              icon="database"
              satellites={[
                { icon: 'box', label: 'Apps' },
                { icon: 'key', label: 'Secrets' },
                { icon: 'archive', label: 'Backups' },
                { icon: 'lock', label: 'Private' },
              ]}
              title="Create a managed database"
              description="PostgreSQL, MySQL, Redis, MongoDB, ClickHouse and more, each with its own resources, private TLS endpoints and backups."
              primary={
                <Button asChild variant="primary">
                  <Link to="/databases/new" search={scope}>
                    <Icon name="plus" size={14} />
                    New database
                  </Link>
                </Button>
              }
              secondary={
                <Button asChild>
                  <Link to="/databases/import" search={scope}>
                    Import a Docker backup
                  </Link>
                </Button>
              }
              steps={[
                {
                  title: 'Pick an engine and size',
                  detail: 'Start small and resize when you need to.',
                },
                {
                  title: 'Connect an application',
                  detail: 'Bind it to a service with Connect application.',
                },
                { title: 'Schedule backups', detail: 'Send copies to your own object storage.' },
              ]}
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
              <p className="db-list-summary">
                <span>
                  {filtered.length} managed {filtered.length === 1 ? 'database' : 'databases'}
                </span>
                {counts.failing > 0 && <span>{counts.failing} need attention</span>}
                {counts.stale > 0 && <span>{counts.stale} stale</span>}
                {counts.provisioning > 0 && <span>{counts.provisioning} provisioning</span>}
                <span>{counts.ready || 0} ready</span>
              </p>
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
                            <Button
                              variant="ghost"
                              size="icon"
                              aria-label={`Actions for ${d.spec.name}`}
                            >
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
                      meta={(() => {
                        const text = `${engineName(d.spec.engine)} ${d.spec.version} · ${databaseLayoutSummary(d.spec)} · ${d.spec.cpu} CPU · ${d.spec.memory} · ${d.spec.storage_gib} GiB per member`
                        return <span title={text}>{text}</span>
                      })()}
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
