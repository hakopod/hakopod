import { useScope, canAccess } from '../lib/scope'
import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { databaseSearch, databaseSummary, databaseHealth, useDatabases } from '../lib/databases'
import { Empty, ErrorState, Loading, PageHeader } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Badge } from '../components/ui/surfaces'

export const Route = createFileRoute('/databases')({
  validateSearch: databaseSearch,
  component: DatabaseRoute,
})
function DatabaseRoute() {
  return useLocation().pathname === '/databases' ? <Databases /> : <Outlet />
}
function Databases() {
  const scope = Route.useSearch()
  const query = useDatabases(scope.project, scope.environment)
  const { identity } = useScope()
  const canManage = !identity.application && canAccess(identity, scope.project, 'deployments:write')
  const [filter, setFilter] = useState('')
  const filtered = (query.data?.items || []).filter((d) =>
    d.spec.name.includes(filter.toLowerCase()),
  )
  return (
    <div className="ops-page">
      <PageHeader
        title="Databases"
        description="PostgreSQL and Redis with independent resources, credentials, backups and lifecycle."
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
      {!scope.project || !scope.environment ? (
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
            <Button onClick={() => void query.refetch()} disabled={query.isFetching}>
              Refresh
            </Button>
          </div>
          {query.isPending ? (
            <Loading />
          ) : query.error ? (
            <ErrorState error={query.error} />
          ) : !query.data.items.length ? (
            <Empty
              title="No managed databases"
              description="Create a PostgreSQL or Redis database with its own resources."
            />
          ) : !filtered.length ? (
            <Empty title="No matching databases" description="Try a different database name." />
          ) : (
            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5">
              {filtered.map((d) => (
                <article key={d.id} className="ops-catalog-card">
                  <div className="flex items-center justify-between gap-2">
                    <h2 className="min-w-0 break-words">
                      <Link
                        to="/databases/$databaseId"
                        params={{ databaseId: d.id }}
                        search={{ project: d.project, environment: d.environment }}
                      >
                        {d.spec.name}
                      </Link>
                    </h2>
                    <Badge>{d.status === 'ready' ? databaseHealth(d) : d.status}</Badge>
                  </div>
                  <p>{databaseSummary(d.spec)}</p>
                  <p>
                    {d.spec.cpu} CPU · {d.spec.memory} memory · {d.spec.storage_gib} GiB per member
                  </p>
                  {d.observation.message && <p role="status">{d.observation.message}</p>}
                </article>
              ))}
            </div>
          )}
        </>
      )}
    </div>
  )
}
