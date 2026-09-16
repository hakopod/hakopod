import { useEffect, useRef, useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { APIError, message, relative } from '../lib/api'
import { useScope } from '../lib/scope'
import { downloadNetworkTOML } from '../lib/virtual-networks'
import { Icon } from '../components/icons'
import {
  HeadingHelp,
  Copy,
  ErrorState,
  Loading,
  Note,
  PageHeader,
  RequestError,
} from '../components/shared'
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
  if (network.error || !network.data)
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
  return (
    <div className="ops-page virtual-network-detail">
      <PageHeader
        title={networkName}
        description={detail.spec.description || 'Private application network'}
        action={
          <div className="toolbar-actions">
            {scope.can('deployments:write') && (
              <Link
                to="/networks/$networkName/connect"
                params={{ networkName }}
                className="button button-primary"
              >
                <Icon name="plus" size={14} />
                Connect service
              </Link>
            )}
            {detail.can_manage && (
              <Link
                to="/networks/$networkName"
                params={{ networkName }}
                search={{ edit: true }}
                className="button button-secondary"
              >
                <Icon name="settings" size={14} />
                Configure
              </Link>
            )}
          </div>
        }
      />
      <div className="network-detail-meta">
        <span>Revision {detail.network.revision}</span>
        <time dateTime={detail.network.updated_at}>
          Updated {relative(detail.network.updated_at)}
        </time>
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
      <section aria-label="Segments" className="network-segment-grid">
        {Object.entries(detail.spec.segments).map(([segment, grants]) => (
          <div className="network-segment-card" key={segment}>
            <h2>
              <Icon name="network" size={15} />
              {segment}
            </h2>
            <span className="muted-text">{grants.applications.length} allowed applications</span>
            <div className="virtual-network-segment-tags">
              {grants.applications.map((application) => (
                <code className="label-chip" key={application}>
                  {application}
                </code>
              ))}
              {!grants.applications.length && <p>No application can join this segment yet.</p>}
            </div>
          </div>
        ))}
      </section>
      <section className="network-connections" aria-labelledby="network-connections-title">
        <div className="section-toolbar">
          <div>
            <div className="hako-section-heading-title">
              <h2 id="network-connections-title">Declared connections</h2>
              <HeadingHelp title="Declared connections">
                Accepted application revisions. Open a service to inspect its live state.
              </HeadingHelp>
            </div>
          </div>
          <span className="label-chip">{detail.connections.length} connections</span>
        </div>
        {detail.connections.length ? (
          <div className="table-container">
            <table>
              <thead>
                <tr>
                  <th>Application / service</th>
                  <th>Segment</th>
                  <th>Private address</th>
                  <th>Revision</th>
                </tr>
              </thead>
              <tbody>
                {detail.connections.map((connection) => (
                  <tr
                    key={`${connection.application_id}:${connection.service}:${connection.network}`}
                  >
                    <td>
                      <Link
                        to="/applications/$applicationId"
                        params={{ applicationId: connection.application_id }}
                        search={{ service: connection.service }}
                      >
                        {connection.application} / {connection.service}
                      </Link>
                      <small className="ops-table-sub">Local network: {connection.network}</small>
                    </td>
                    <td>
                      <code>{connection.segment}</code>
                    </td>
                    <td>
                      <div className="copyable-address">
                        <code>{connection.address}</code>
                        <Copy value={connection.address} />
                      </div>
                      <small className="ops-table-sub">
                        {connection.ports.length
                          ? connection.ports
                              .map((port) => `${port.port}/${port.protocol}`)
                              .join(' · ')
                          : 'No inbound port'}
                      </small>
                    </td>
                    <td className="mono">r{connection.revision}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Note>
            No service has declared a connection yet. Grant an application access, then connect one
            of its services.
          </Note>
        )}
        {detail.truncated && (
          <Note>
            Showing the first 256 declared connections. Inspect individual applications for the
            remaining connections.
          </Note>
        )}
      </section>
      <section className="network-configuration">
        <div className="section-toolbar">
          <h2>Network configuration</h2>
          <Button size="sm" onClick={() => downloadNetworkTOML(networkName, detail.toml)}>
            <Icon name="download" size={14} />
            Export TOML
          </Button>
        </div>
        <TOMLCode code={detail.toml} />
      </section>
      {detail.can_manage && (
        <div className="network-delete-row">
          <p>Disconnect services before deleting a network.</p>
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
        description="Remove this network and its grants from the current project and environment."
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
