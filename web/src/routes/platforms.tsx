import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
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
import { Icon } from '../components/icons'
import { ServiceIcon } from '../components/service-icon'
import { DatabaseResourceNavigation } from '../components/database-resource-navigation'
import { useScope, canAccess } from '../lib/scope'
import {
  availableManagedPlatformEntries,
  managedPlatformName,
  platformSearch,
  useManagedPlatformCatalog,
  useManagedPlatforms,
} from '../lib/managed-platforms'
import { projectRouteScopeMatches } from '../lib/projects'

export const Route = createFileRoute('/platforms')({
  validateSearch: platformSearch,
  component: Page,
})
function Page() {
  return useLocation().pathname === '/platforms' ? <Platforms /> : <Outlet />
}
function Platforms() {
  const scope = Route.useSearch()
  const workspace = useScope()
  const requestedScope = Boolean(scope.project || scope.environment)
  const scopeMatches = !requestedScope || projectRouteScopeMatches(workspace, scope)
  const query = useManagedPlatforms(scope.project, scope.environment, scopeMatches)
  const canManage =
    scopeMatches &&
    !workspace.identity.application &&
    canAccess(workspace.identity, scope.project, 'deployments:write')
  const catalog = useManagedPlatformCatalog(scope.project, scope.environment, canManage)
  const canCreate = Boolean(catalog.data && availableManagedPlatformEntries(catalog.data).length)
  return (
    <div className="ops-page">
      <PageHeader
        title="Platforms"
        description="Owned application platforms with durable revisions and operations."
        action={
          scopeMatches ? (
            <div className="flex flex-wrap gap-2">
              {canCreate && scope.project && scope.environment && (
                <Button asChild variant="primary">
                  <Link to="/platforms/new" search={scope}>
                    New platform
                  </Link>
                </Button>
              )}
              <Button onClick={() => void query.refetch()} disabled={query.isFetching}>
                <Icon name="refresh" size={14} />
                Refresh
              </Button>
            </div>
          ) : undefined
        }
      />
      <DatabaseResourceNavigation active="platforms" scope={scope} />
      {!scopeMatches ? (
        <Empty
          title="Workspace unavailable"
          description="Choose an accessible project and environment above."
        />
      ) : (
        <>
          {query.isPending ? (
            <Loading />
          ) : query.error && !query.data ? (
            <ResourceError
              error={query.error}
              retry={() => void query.refetch()}
              subject="managed platforms"
            />
          ) : !query.data.items.length ? (
            <ResourceEmpty
              icon="server"
              title="No managed platforms"
              description="No managed platforms are available in your accessible projects."
              action={
                canCreate &&
                scope.project &&
                scope.environment && (
                  <Button asChild>
                    <Link to="/platforms/new" search={scope}>
                      New platform
                    </Link>
                  </Button>
                )
              }
            />
          ) : (
            <>
              {query.error && (
                <StaleNotice updatedAt={query.dataUpdatedAt} retry={() => void query.refetch()} />
              )}
              <div className="resource-grid" aria-label="Managed platforms">
                {query.data.items.map((item) => (
                  <ResourceCard
                    key={item.id}
                    cue={<ServiceIcon name={item.spec.kind} size={20} />}
                    name={
                      <Link
                        className="ops-card-link"
                        to="/platforms/$platformId"
                        params={{ platformId: item.id }}
                        search={{ project: item.project, environment: item.environment }}
                      >
                        {item.spec.name}
                      </Link>
                    }
                    meta={
                      <span title={item.spec.version}>
                        {managedPlatformName(item.spec.kind)}{' '}
                        {item.spec.kind === 'neon'
                          ? item.spec.version.slice(0, 12)
                          : item.spec.version}{' '}
                        · r{item.revision}
                      </span>
                    }
                    endpoint={
                      <span>
                        {item.project} / {item.environment}
                      </span>
                    }
                    state={
                      <ObservedState
                        status={item.status}
                        freshness={
                          item.observation.revision
                            ? `Observed for r${item.observation.revision}`
                            : 'Not observed'
                        }
                      />
                    }
                    metric={
                      <CardMetric
                        label="Ready components"
                        window="now"
                        value={
                          typeof item.observation.ready_components === 'number' &&
                          typeof item.observation.expected_components === 'number'
                            ? `${item.observation.ready_components} / ${item.observation.expected_components}`
                            : undefined
                        }
                      />
                    }
                  />
                ))}
              </div>
            </>
          )}
        </>
      )}
    </div>
  )
}
