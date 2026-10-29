import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { relative } from '../lib/api'
import { useScope } from '../lib/scope'
import { Icon } from '../components/icons'
import { Loading, PageHeader } from '../components/shared'
import {
  ResourceEmpty,
  ResourceError,
  ResourceRow,
  ResourceRows,
  StaleNotice,
} from '../components/resource-card'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { dashboardEdition } from '../lib/dashboard-edition'

export const Route = createFileRoute('/networks')({ component: NetworkRoute })
function NetworkRoute() {
  return useLocation().pathname === '/networks' ? <Networks /> : <Outlet />
}
function Networks() {
  const scope = useScope()
  const [search, setSearch] = useState('')
  const networks = useQuery({
    queryKey: ['virtual-networks', scope.project, scope.environment],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/virtual-networks', {
          signal,
          params: { query: { project: scope.project, environment: scope.environment } },
        }),
      ),
    enabled: Boolean(scope.project && scope.environment),
    staleTime: 15000,
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
  const items = networks.data?.items || []
  const filtered = items.filter((item) =>
    `${item.name} ${item.description}`.toLowerCase().includes(search.toLowerCase()),
  )
  return (
    <div className="ops-page virtual-networks-page">
      <PageHeader
        title="Virtual networks"
        description="Private connections between applications in this project and environment."
        action={
          networks.data?.can_manage && (
            <Button asChild variant="primary">
              <Link to="/networks/new">
                <Icon name="plus" size={14} />
                New network
              </Link>
            </Button>
          )
        }
      />
      <section className="ops-resource-list resource-section" aria-label="Virtual networks">
        <div className="resource-toolbar">
          <div className="ops-search-input">
            <Icon name="search" size={15} />
            <Input
              aria-label="Filter networks"
              placeholder="Find network…"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </div>
          <span className="form-spacer" />
          <Button
            size="icon"
            variant="ghost"
            aria-label="Refresh networks"
            disabled={networks.isFetching}
            onClick={() => void networks.refetch()}
          >
            <Icon name="refresh" size={15} className={networks.isFetching ? 'spin' : ''} />
          </Button>
        </div>
        {networks.isPending ? (
          <Loading />
        ) : networks.error && !networks.data ? (
          <ResourceError
            error={networks.error}
            retry={() => void networks.refetch()}
            subject="networks in this environment"
          />
        ) : !items.length ? (
          <ResourceEmpty
            icon="network"
            title="No virtual networks"
            description={
              networks.data?.can_manage
                ? 'A network lets granted applications connect their services privately.'
                : dashboardEdition.cloud
                  ? 'Your workspace owner can manage private networks. The connected node key must include networks:write, deployment read and write permissions.'
                  : 'A project administrator can create a network and grant applications access.'
            }
            action={
              networks.data?.can_manage && (
                <Button asChild>
                  <Link to="/networks/new">Create a network</Link>
                </Button>
              )
            }
          />
        ) : !filtered.length ? (
          <ResourceEmpty
            icon="search"
            title="No matching networks"
            description="Try another name or description."
          />
        ) : (
          <>
            {networks.error && (
              <StaleNotice
                updatedAt={networks.dataUpdatedAt}
                retry={() => void networks.refetch()}
              />
            )}
            <ResourceRows label="Virtual networks">
              {filtered.map((network) => (
                <ResourceRow
                  key={network.name}
                  identity={
                    <Link
                      to="/networks/$networkName"
                      params={{ networkName: network.name }}
                      className="resource-row-link"
                      aria-label={`Open ${network.name} network`}
                    >
                      {network.name}
                    </Link>
                  }
                  detail={
                    <>
                      <span>{network.description || 'Private application network'}</span>
                      <span>r{network.revision}</span>
                      {network.segments.slice(0, 4).map((segment) => (
                        <code key={segment}>{segment}</code>
                      ))}
                      {network.segments.length > 4 && (
                        <span>+{network.segments.length - 4} more</span>
                      )}
                    </>
                  }
                  status={
                    <span className="resource-row-count">
                      {network.segments.length}{' '}
                      {network.segments.length === 1 ? 'segment' : 'segments'}
                    </span>
                  }
                  meta={
                    <>
                      Updated{' '}
                      <time dateTime={network.updated_at} title={network.updated_at}>
                        {relative(network.updated_at).toLowerCase()}
                      </time>
                    </>
                  }
                />
              ))}
            </ResourceRows>
          </>
        )}
        <div className="resource-footnote">
          <span>{items.length} networks</span>
          <span>
            Grants are managed by{' '}
            {dashboardEdition.cloud ? 'workspace owners' : 'project administrators'}
          </span>
        </div>
      </section>
    </div>
  )
}
