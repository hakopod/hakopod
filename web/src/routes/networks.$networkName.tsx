import { useEffect, useRef, useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { APIError, message, relative, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { downloadNetworkTOML } from '../lib/virtual-networks'
import { Icon } from '../components/icons'
import {
  Copy,
  ErrorState,
  Loading,
  Note,
  PageHeader,
  RequestError,
  Status,
} from '../components/shared'
import {
  ResourceEmpty,
  ResourceRow,
  ResourceRows,
  StaleNotice,
} from '../components/resource-card'
import { plural } from '../components/create-flow'
import { ScopeLabel } from '../components/task-review'
import { NetworkTopology } from '../components/network-topology'
import { TOMLCode } from '../components/toml-code'
import { VirtualNetworkForm } from '../components/virtual-network-form'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Input } from '../components/ui/input'

export const Route = createFileRoute('/networks/$networkName')({
  validateSearch: (search: Record<string, unknown>): { edit?: boolean } => ({
    edit: search.edit === true || search.edit === 'true' || undefined,
  }),
  component: NetworkRoute,
})
function NetworkRoute() {
  const { networkName } = Route.useParams()
  const scope = useScope()
  return useLocation().pathname === `/networks/${networkName}` ? (
    <NetworkDetail key={`${scope.project}:${scope.environment}:${networkName}`} />
  ) : (
    <Outlet />
  )
}
function NetworkDetail() {
  const { networkName } = Route.useParams()
  const { edit } = Route.useSearch()
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [deleteBase, setDeleteBase] = useState<{ id: string; revision: number } | null>(null)
  const [deleteStale, setDeleteStale] = useState(false)
  const deleteRequest = useRef<AbortController | null>(null)
  useEffect(() => () => deleteRequest.current?.abort(), [])
  const network = useQuery({
    queryKey: ['virtual-network', scope.project, scope.environment, networkName],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/virtual-networks/{name}', {
          signal,
          params: {
            path: { name: networkName },
            query: { project: scope.project, environment: scope.environment },
          },
        }),
      ),
    enabled: Boolean(scope.project && scope.environment),
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
  if (network.isPending) return <Loading />
  if (!network.data)
    return <ErrorState error={network.error} retry={() => void network.refetch()} />
  const detail = network.data
  if (edit && detail.can_manage)
    return (
      <VirtualNetworkForm
        project={scope.project}
        environment={scope.environment}
        existing={detail}
      />
    )
  const segments = Object.entries(detail.spec.segments)
  const canConnect = scope.can('deployments:write')
  return (
    <div className="ops-page virtual-network-detail">
      <PageHeader
        title={networkName}
        description={detail.spec.description || 'Private application network'}
        action={
          <div className="toolbar-actions">
            {detail.can_manage && (
              <Button asChild>
                <Link to="/networks/$networkName" params={{ networkName }} search={{ edit: true }}>
                  <Icon name="settings" size={14} />
                  Configure grants
                </Link>
              </Button>
            )}
            {canConnect && (
              <Button asChild variant="primary">
                <Link to="/networks/$networkName/connect" params={{ networkName }}>
                  <Icon name="plus" size={14} />
                  Connect service
                </Link>
              </Button>
            )}
          </div>
        }
      />
      <div className="network-detail-meta">
        <ScopeLabel project={scope.project} environment={scope.environment} />
        <span className="mono">r{detail.network.revision}</span>
        <time dateTime={detail.network.updated_at} title={timestamp(detail.network.updated_at)}>
          Updated {relative(detail.network.updated_at).toLowerCase()}
        </time>
        <span>
          {plural(segments.length, 'segment')} ·{' '}
          {plural(detail.connections.length, 'connected service')}
          {detail.truncated ? ' (first 256 shown)' : ''}
        </span>
        <Button
          variant="ghost"
          size="sm"
          disabled={network.isFetching}
          onClick={() => void network.refetch()}
        >
          <Icon name="refresh" size={14} className={network.isFetching ? 'spin' : ''} />
          Refresh
        </Button>
      </div>
      {network.error && (
        <StaleNotice updatedAt={network.dataUpdatedAt} retry={() => void network.refetch()} />
      )}
      <p className="network-reach-rule">
        Services connected to the same segment can reach each other at their private addresses,
        subject to each service’s peer restrictions. A service reaches only the segments it has joined.
      </p>
      {segments.length > 0 && <NetworkTopology detail={detail} />}
      <div className="network-segments">
        {segments.map(([segment, grants]) => {
          const connected = detail.connections.filter((item) => item.segment === segment)
          const ungranted = connected.filter(
            (item) => !grants.applications.includes(item.application),
          )
          return (
            <section
              key={segment}
              className="network-segment"
              aria-labelledby={`segment-${segment}`}
            >
              <header className="network-segment-heading">
                <Icon name="network" size={15} />
                <h2 id={`segment-${segment}`}>{segment}</h2>
                <span>
                  {plural(grants.applications.length, 'application')} allowed ·{' '}
                  {plural(connected.length, 'service')} connected
                </span>
              </header>
              {grants.applications.length || connected.length ? (
                <ResourceRows label={`Applications in ${segment}`}>
                  {[
                    ...grants.applications,
                    ...new Set(ungranted.map((item) => item.application)),
                  ].map((application) => {
                    const services = connected.filter((item) => item.application === application)
                    const allowed = grants.applications.includes(application)
                    return services.length ? (
                      services.map((connection) => (
                        <ResourceRow
                          key={`${connection.application_id}:${connection.service}:${connection.network}`}
                          identity={
                            <Link
                              className="resource-row-link"
                              to="/applications/$applicationId"
                              params={{ applicationId: connection.application_id }}
                              search={{ service: connection.service }}
                            >
                              {connection.application} / {connection.service}
                            </Link>
                          }
                          detail={
                            <>
                              <span className="copy-id">
                                <code>{connection.address}</code>
                                <Copy
                                  value={connection.address}
                                  label={`Copy private address of ${connection.application} / ${connection.service}`}
                                  iconOnly
                                />
                              </span>
                              <span>
                                {connection.ports.length
                                  ? connection.ports
                                      .map((port) => `${port.port}/${port.protocol || 'TCP'}`)
                                      .join(' · ')
                                  : 'No inbound port'}
                              </span>
                              <span>Local network {connection.network}</span>
                              {!allowed && (
                                <span className="network-warning-text">
                                  No longer granted in this segment
                                </span>
                              )}
                            </>
                          }
                          status={<Status value={connection.status || 'unknown'} small />}
                          meta={<span className="mono">r{connection.revision}</span>}
                          actions={
                            canConnect && (
                              <Button asChild size="sm" variant="ghost">
                                <Link
                                  to="/networks/$networkName/connect"
                                  params={{ networkName }}
                                  search={{
                                    disconnect: `${connection.application_id}/${connection.service}/${connection.network}`,
                                  }}
                                  aria-label={`Disconnect ${connection.application} / ${connection.service}`}
                                >
                                  Disconnect
                                </Link>
                              </Button>
                            )
                          }
                        />
                      ))
                    ) : (
                      <ResourceRow
                        key={application}
                        identity={<span>{application}</span>}
                        detail={<span>Allowed to join · no service connected</span>}
                        status={<span className="resource-row-count">Not connected</span>}
                        actions={
                          canConnect && (
                            <Button asChild size="sm" variant="ghost">
                              <Link
                                to="/networks/$networkName/connect"
                                params={{ networkName }}
                                search={{ segment, application }}
                                aria-label={`Connect a service of ${application} to ${segment}`}
                              >
                                Connect
                              </Link>
                            </Button>
                          )
                        }
                      />
                    )
                  })}
                </ResourceRows>
              ) : (
                <ResourceEmpty
                  icon="lock"
                  title="No application can join this segment"
                  description={
                    detail.can_manage
                      ? 'Grant an application to let its services connect.'
                      : 'A project administrator can grant applications access.'
                  }
                  action={
                    detail.can_manage && (
                      <Button asChild size="sm">
                        <Link
                          to="/networks/$networkName"
                          params={{ networkName }}
                          search={{ edit: true }}
                        >
                          Grant applications
                        </Link>
                      </Button>
                    )
                  }
                />
              )}
            </section>
          )
        })}
      </div>
      <details className="network-configuration">
        <summary>
          <Icon name="chevron" size={14} />
          <span>Network TOML</span>
          <span className="muted-text">r{detail.network.revision}</span>
        </summary>
        <div className="network-configuration-body">
          <Button size="sm" onClick={() => downloadNetworkTOML(networkName, detail.toml)}>
            <Icon name="download" size={14} />
            Export TOML
          </Button>
          <TOMLCode code={detail.toml} />
        </div>
      </details>
      {detail.can_manage && (
        <div className="network-delete-row">
          <p>
            {detail.connections.length
              ? `Disconnect all ${plural(detail.connections.length, 'service')} before deleting this network.`
              : 'No service is connected. Deleting removes the network and its grants.'}
          </p>
          <Button
            variant="danger"
            size="sm"
            onClick={() => {
              if (!deleteStale)
                setDeleteBase({ id: detail.network.id, revision: detail.network.revision })
              setDeleteOpen(true)
            }}
          >
            Delete network
          </Button>
        </div>
      )}
      <Dialog
        open={deleteOpen}
        onOpenChange={(open) => {
          if (!busy) setDeleteOpen(open)
        }}
        title={`Delete ${networkName}?`}
        description={`Removes ${networkName} and the grants of its ${plural(Object.keys(detail.spec.segments).length, 'segment')} from ${scope.project} / ${scope.environment}. Applications are not changed.`}
      >
        <div className="dialog-body">
          <Note>
            Deleting revision r{deleteBase?.revision}. The server blocks deletion while application
            revisions still reference this network.
          </Note>
          <label className="field-stack">
            Type {networkName} to confirm
            <Input
              aria-label="Confirm network name"
              value={confirmation}
              autoComplete="off"
              spellCheck={false}
              disabled={busy}
              onChange={(event) => setConfirmation(event.target.value)}
            />
          </label>
          {deleteStale && (
            <div className="network-base-conflict">
              <Note>
                The network changed after this review. Load the current network before confirming
                deletion again.
              </Note>
              <Button
                disabled={busy}
                onClick={async () => {
                  setBusy(true)
                  const controller = new AbortController()
                  deleteRequest.current = controller
                  try {
                    const current = await unwrap(
                      client.GET('/virtual-networks/{name}', {
                        signal: controller.signal,
                        params: {
                          path: { name: networkName },
                          query: { project: scope.project, environment: scope.environment },
                        },
                      }),
                    )
                    setDeleteBase({ id: current.network.id, revision: current.network.revision })
                    setDeleteStale(false)
                    setError('')
                    cache.setQueryData(
                      ['virtual-network', scope.project, scope.environment, networkName],
                      current,
                    )
                  } catch (cause) {
                    if (!controller.signal.aborted) setError(message(cause))
                  } finally {
                    if (!controller.signal.aborted) setBusy(false)
                  }
                }}
              >
                Load current network
              </Button>
            </div>
          )}
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setDeleteOpen(false)}>
            Cancel
          </Button>
          <Button
            variant="danger"
            disabled={busy || deleteStale || !deleteBase || confirmation !== networkName}
            onClick={async () => {
              if (!deleteBase) return
              setBusy(true)
              setError('')
              const controller = new AbortController()
              deleteRequest.current = controller
              try {
                await unwrap(
                  client.DELETE('/virtual-networks/{name}', {
                    signal: controller.signal,
                    params: { path: { name: networkName } },
                    body: {
                      project: scope.project,
                      environment: scope.environment,
                      expected_revision: deleteBase.revision,
                      expected_id: deleteBase.id,
                      confirmation,
                    },
                  }),
                )
                void cache.invalidateQueries({
                  queryKey: ['virtual-networks', scope.project, scope.environment],
                })
                void navigate({ to: '/networks' })
              } catch (cause) {
                if (!controller.signal.aborted) {
                  setError(message(cause))
                  if (cause instanceof APIError && cause.status === 409) setDeleteStale(true)
                }
              } finally {
                if (!controller.signal.aborted) setBusy(false)
              }
            }}
          >
            {busy ? 'Deleting…' : 'Delete network'}
          </Button>
        </div>
      </Dialog>
    </div>
  )
}
