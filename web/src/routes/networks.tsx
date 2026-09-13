import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { client, unwrap } from '../lib/client'
import { relative } from '../lib/api'
import { useScope } from '../lib/scope'
import { Icon } from '../components/icons'
import { Empty, ErrorState, Loading, PageHeader } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Badge } from '../components/ui/surfaces'

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
            <Link to="/networks/new" className="button button-primary">
              <Icon name="plus" size={14} />
              New network
            </Link>
          )
        }
      />
      <section className="ops-resource-list" aria-label="Virtual networks">
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
        ) : networks.error ? (
          <ErrorState error={networks.error} retry={() => void networks.refetch()} />
        ) : !items.length ? (
          <Empty
            icon="network"
            title="Connect applications privately"
            description={
              networks.data?.can_manage
                ? 'Create a network, grant applications access to its segments, then connect their services.'
                : 'A project administrator can create a network and grant applications access.'
            }
            action={
              networks.data?.can_manage && (
                <Link to="/networks/new" className="button button-secondary">
                  Create a network
                </Link>
              )
            }
          />
        ) : !filtered.length ? (
          <Empty
            icon="search"
            title="No matching networks"
            description="Try another name or description."
          />
        ) : (
          <div className="ops-catalog-grid">
            {filtered.map((network) => (
              <article className="ops-catalog-card interactive" key={network.name}>
                <Brackets />
                <div className="ops-card-heading">
                  <Icon name="network" size={23} />
                  <Badge>r{network.revision}</Badge>
                </div>
                <h2>
                  <Link
                    to="/networks/$networkName"
                    params={{ networkName: network.name }}
                    className="ops-card-link"
                    aria-label={`Open ${network.name} network`}
                  >
                    {network.name}
                  </Link>
                </h2>
                <p className="virtual-network-description">
                  {network.description || 'Private application network'}
                </p>
                <div className="virtual-network-segment-tags">
                  {network.segments.slice(0, 4).map((segment) => (
                    <span className="label-chip" key={segment}>
                      {segment}
                    </span>
                  ))}
                  {network.segments.length > 4 && (
                    <span className="label-chip">+{network.segments.length - 4} segments</span>
                  )}
                </div>
                <div className="ops-card-footer">
                  <time dateTime={network.updated_at} title={network.updated_at}>
                    {relative(network.updated_at)}
                  </time>
                  <Icon name="arrow" size={15} />
                </div>
              </article>
            ))}
          </div>
        )}
        <div className="resource-footnote">
          <span>{items.length} networks</span>
          <span>Grants are managed by project administrators</span>
        </div>
      </section>
    </div>
  )
}
