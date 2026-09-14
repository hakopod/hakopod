import {
  InstallationLogs,
  InstallationSetup,
  InstallationUpdates,
  useInstallationOwner,
} from '../components/installation'
import { Input } from '../components/ui/input'
import { lazy, Suspense, useEffect, useState } from 'react'
import { Badge } from '../components/ui/surfaces'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { Dialog } from '../components/ui/dialog'
import * as Tabs from '@radix-ui/react-tabs'
import { useScope, canOpenHostTerminal } from '../lib/scope'
import { useActiveSection } from '../lib/use-active-section'
import type { Node } from '../lib/types'
import { createFileRoute, Outlet, useLocation, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { Icon } from '../components/icons'
import { Button } from '../components/ui/button'
import { Copy, Empty, ErrorState, Loading, Note, PageHeader, Status } from '../components/shared'
import { NodeMetrics } from '../components/node-metrics'

const NodeEnrollments = lazy(() => import('../components/node-controls'))
const NodeAction = lazy(() =>
  import('../components/node-controls').then((m) => ({ default: m.NodeAction })),
)
const RegistrySettings = lazy(() => import('../components/registry-settings'))
const IssuerSettings = lazy(() => import('../components/tls-settings'))
const ProxySettings = lazy(() => import('../components/proxy-settings'))

export const Route = createFileRoute('/infrastructure')({
  validateSearch: (search: Record<string, unknown>): { tab?: string } => ({
    tab: ['nodes', 'registries', 'tls', 'enrollment', 'proxy', 'logs', 'setup', 'updates'].includes(
      String(search.tab),
    )
      ? String(search.tab)
      : undefined,
  }),
  component: InfrastructureRoute,
})
function InfrastructureRoute() {
  return useLocation().pathname === '/infrastructure' ? <Infrastructure /> : <Outlet />
}
function cpu(quantity: string) {
  return quantity.endsWith('m') ? Number(quantity.slice(0, -1)) / 1000 : Number(quantity)
}
function memory(quantity: string) {
  const parsed = /^(\d+(?:\.\d+)?)([KMGTE]i?)?$/.exec(quantity)
  if (!parsed) return NaN
  const unit = parsed[2] || ''
  const power = ['K', 'M', 'G', 'T', 'E'].indexOf(unit[0]) + 1
  return Number(parsed[1]) * (unit.endsWith('i') ? 1024 : 1000) ** power
}
function gib(bytes: number) {
  return Number.isFinite(bytes)
    ? `${(bytes / 1024 ** 3).toLocaleString(undefined, { maximumFractionDigits: 1 })} GiB`
    : '—'
}

function Infrastructure() {
  const owner = useInstallationOwner()
  const scope = useScope()
  const { tab } = Route.useSearch()
  const navigationRoot = useActiveSection(tab || 'nodes', '.tab-list')
  const navigate = Route.useNavigate()
  return (
    <div className="ops-page">
      <PageHeader
        eyebrow="OPERATOR / INFRASTRUCTURE"
        title="Infrastructure"
        description="Live capacity, node scheduling, registries, and public ingress."
        action={
          <div className="toolbar-actions">
            {scope.identity.owner && (
              <Link className="button button-secondary" to="/settings/host-access">
                <Icon name="key" size={15} />
                Host access
              </Link>
            )}
            {scope.identity.admin && (!tab || tab === 'nodes') && (
              <Link
                className="button button-primary"
                to="/infrastructure"
                search={{ tab: 'enrollment' }}
              >
                <Icon name="plus" size={15} />
                Add node
              </Link>
            )}
          </div>
        }
      />
      <Tabs.Root
        value={tab || 'nodes'}
        onValueChange={(value) => void navigate({ search: { tab: value } })}
      >
        <Tabs.List ref={navigationRoot} className="tab-list" aria-label="Infrastructure sections">
          <Tabs.Trigger className="tab-trigger" value="nodes">
            Nodes
          </Tabs.Trigger>
          <Tabs.Trigger className="tab-trigger" value="registries">
            Registries
          </Tabs.Trigger>
          <Tabs.Trigger className="tab-trigger" value="tls">
            Certificates
          </Tabs.Trigger>
          {scope.identity.admin && (
            <Tabs.Trigger className="tab-trigger" value="enrollment">
              Add workers
            </Tabs.Trigger>
          )}
          {scope.identity.admin && (
            <Tabs.Trigger className="tab-trigger" value="proxy">
              HAProxy
            </Tabs.Trigger>
          )}
          {owner && (
            <>
              <Tabs.Trigger className="tab-trigger" value="logs">
                API logs
              </Tabs.Trigger>
              <Tabs.Trigger className="tab-trigger" value="setup">
                Setup
              </Tabs.Trigger>
              <Tabs.Trigger className="tab-trigger" value="updates">
                Updates
              </Tabs.Trigger>
            </>
          )}
        </Tabs.List>
        <Tabs.Content className="tab-content" value="nodes">
          <Nodes />
        </Tabs.Content>
        <Tabs.Content className="tab-content" value="registries">
          <Suspense fallback={<Loading />}>
            <RegistrySettings />
          </Suspense>
        </Tabs.Content>
        <Tabs.Content className="tab-content" value="tls">
          <Suspense fallback={<Loading />}>
            <IssuerSettings />
          </Suspense>
        </Tabs.Content>
        {scope.identity.admin && (
          <Tabs.Content className="tab-content" value="enrollment">
            <Suspense fallback={<Loading />}>
              <NodeEnrollments />
            </Suspense>
          </Tabs.Content>
        )}
        {scope.identity.admin && (
          <Tabs.Content className="tab-content" value="proxy">
            <Suspense fallback={<Loading />}>
              <ProxySettings />
            </Suspense>
          </Tabs.Content>
        )}
        <Tabs.Content className="tab-content" value="logs">
          <InstallationLogs />
        </Tabs.Content>
        <Tabs.Content className="tab-content" value="setup">
          <InstallationSetup />
        </Tabs.Content>
        <Tabs.Content className="tab-content" value="updates">
          <InstallationUpdates />
        </Tabs.Content>
      </Tabs.Root>
    </div>
  )
}
function Nodes() {
  const scope = useScope()
  const cache = useQueryClient()
  const [action, setAction] = useState<{ node: Node; action: 'cordon' | 'drain' } | null>(null)
  const [search, setSearch] = useState('')
  const [selectedNode, setSelectedNode] = useState('')
  const [paused, setPaused] = useState(false)
  const [visible, setVisible] = useState(false)
  useEffect(() => {
    const change = () => setVisible(!document.hidden)
    document.addEventListener('visibilitychange', change)
    change()
    return () => document.removeEventListener('visibilitychange', change)
  }, [])
  const polling = visible && !paused
  const nodes = useQuery({
    queryKey: ['nodes'],
    queryFn: ({ signal }) => unwrap(client.GET('/nodes', { signal })),
    enabled: polling,
    refetchInterval: polling ? (selectedNode ? 15000 : 30000) : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false,
    retry: false,
    gcTime: 0,
  })
  useEffect(() => {
    if (!polling) void cache.cancelQueries({ queryKey: ['nodes'], exact: true })
  }, [polling, cache])
  const probe = () => void nodes.refetch({ cancelRefetch: false })
  const items = nodes.data?.items || []
  const filtered = items.filter((node) => node.name.toLowerCase().includes(search.toLowerCase()))
  const inspector = items.find((node) => node.name === selectedNode)
  const totalCPU = items.reduce((sum, node) => sum + cpu(node.allocatable_cpu), 0)
  const totalMemory = items.reduce((sum, node) => sum + memory(node.allocatable_memory), 0)
  return (
    <>
      <dl className="ops-summary ops-summary-three" aria-label="Cluster capacity">
        <div>
          <dt>Ready nodes</dt>
          <dd>
            {nodes.data ? `${items.filter((node) => node.ready).length} / ${items.length}` : '—'}
          </dd>
          <small>Kubernetes readiness condition</small>
        </div>
        <div>
          <dt>Allocatable CPU</dt>
          <dd>
            {nodes.data && Number.isFinite(totalCPU)
              ? `${totalCPU.toLocaleString(undefined, { maximumFractionDigits: 2 })} cores`
              : '—'}
          </dd>
          <small>Available to workloads</small>
        </div>
        <div>
          <dt>Allocatable memory</dt>
          <dd>{nodes.data ? gib(totalMemory) : '—'}</dd>
          <small>After system reservations</small>
        </div>
      </dl>
      <section className="ops-resource-list" aria-label="Cluster nodes">
        <div className="resource-toolbar">
          <div className="ops-search-input">
            <Icon name="search" size={15} />
            <Input
              aria-label="Search nodes"
              placeholder="Find a node…"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </div>
          <span className="muted-text">{items.length} nodes</span>
          <span className="form-spacer" />
          <Button variant="ghost" size="sm" onClick={() => setPaused((value) => !value)}>
            <Icon name={paused ? 'play' : 'pause'} size={15} />
            {paused ? 'Resume live' : 'Pause live'}
          </Button>
          <Button variant="ghost" size="sm" disabled={nodes.isFetching || !visible} onClick={probe}>
            <Icon name="refresh" size={15} className={nodes.isFetching ? 'spin' : ''} />
            {nodes.isFetching ? 'Probing…' : 'Probe now'}
          </Button>
        </div>
        {nodes.isPending ? (
          <Loading />
        ) : nodes.error ? (
          <ErrorState error={nodes.error} retry={probe} />
        ) : !items.length ? (
          <Empty
            icon="server"
            title="No nodes reported"
            description="Check the management API’s Kubernetes context and cluster connectivity. Nodes appear when the cluster returns them."
          />
        ) : !filtered.length ? (
          <Empty icon="search" title="No matching nodes" description="Try a different node name." />
        ) : (
          <div className="table-container ops-table">
            <table>
              <thead>
                <tr>
                  <th>Node / state</th>
                  <th>CPU</th>
                  <th>Memory</th>
                  <th>Pods / GPUs</th>
                  <th>Kubernetes</th>
                  <th>
                    <span className="sr-only">Node actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((node) => (
                  <tr key={node.name}>
                    <td>
                      <div className="ops-object">
                        <Status value={node.ready ? 'ready' : 'not ready'} small />
                        <button
                          type="button"
                          className="ops-object-name"
                          onClick={() => setSelectedNode(node.name)}
                        >
                          {node.name}
                        </button>
                      </div>
                      <small className="ops-table-sub">
                        {node.control_plane ? 'Control plane' : 'Worker'} · {node.architecture} ·{' '}
                        {node.unschedulable ? 'Cordoned' : 'Schedulable'}
                      </small>
                    </td>
                    <td>
                      <span className="mono">
                        {node.metrics.available && node.metrics.cpu_millicores !== undefined
                          ? `${node.metrics.cpu_millicores.toFixed(0)} mCPU used`
                          : 'Usage unavailable'}
                      </span>
                      <small className="ops-table-sub mono">
                        {cpu(node.allocatable_cpu)} cores allocatable
                      </small>
                    </td>
                    <td>
                      <span className="mono">
                        {node.metrics.available && node.metrics.memory_bytes !== undefined
                          ? `${gib(node.metrics.memory_bytes)} used`
                          : 'Usage unavailable'}
                      </span>
                      <small className="ops-table-sub mono">
                        {gib(memory(node.allocatable_memory))} allocatable
                      </small>
                    </td>
                    <td className="mono">
                      {node.pods} pods
                      <small className="ops-table-sub">{node.allocatable_gpu} GPUs</small>
                    </td>
                    <td>
                      <code>{node.kubelet_version}</code>
                    </td>
                    <td>
                      <Menu
                        trigger={
                          <Button
                            size="icon"
                            variant="ghost"
                            aria-label={`Actions for ${node.name}`}
                          >
                            <span aria-hidden="true">···</span>
                          </Button>
                        }
                      >
                        <MenuItem onSelect={() => setSelectedNode(node.name)}>
                          <Icon name="server" size={14} />
                          Inspect node
                        </MenuItem>
                        {scope.identity.admin && !node.control_plane && (
                          <>
                            <MenuItem
                              onSelect={() =>
                                setAction({ node: structuredClone(node), action: 'cordon' })
                              }
                            >
                              {node.unschedulable ? 'Uncordon' : 'Cordon'}
                            </MenuItem>
                            <MenuItem
                              destructive
                              onSelect={() =>
                                setAction({ node: structuredClone(node), action: 'drain' })
                              }
                            >
                              Review drain
                            </MenuItem>
                          </>
                        )}
                      </Menu>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="resource-footnote">
          <span>
            {nodes.dataUpdatedAt
              ? `Refreshed ${timestamp(new Date(nodes.dataUpdatedAt).toISOString())}`
              : 'Waiting for cluster observation'}
          </span>
          <span>
            {paused
              ? 'Live updates paused'
              : `Checks every ${selectedNode ? 15 : 30} seconds while this page is visible`}
          </span>
        </div>
      </section>
      <Dialog
        sheet
        open={Boolean(inspector)}
        onOpenChange={(open) => {
          if (!open) setSelectedNode('')
        }}
        title={inspector?.name || 'Node'}
        description="Observed capacity, usage, and scheduling state."
      >
        {inspector && (
          <>
            <div className="dialog-body ops-node-inspector">
              <div className="ops-object">
                <Status value={inspector.ready ? 'ready' : 'not ready'} />
                <Badge>{inspector.control_plane ? 'Control plane' : 'Worker'}</Badge>
                <Copy value={inspector.name} label="Copy node name" />
              </div>
              <NodeMetrics
                key={inspector.name}
                metrics={inspector.metrics}
                cpuCapacity={cpu(inspector.allocatable_cpu) * 1000}
                memoryCapacity={memory(inspector.allocatable_memory)}
                observedAt={nodes.data?.observed_at}
                receivedAt={nodes.dataUpdatedAt}
                paused={paused}
                visible={visible}
                fetching={nodes.isFetching}
                error={nodes.error}
                onPause={() => setPaused((value) => !value)}
                onProbe={probe}
              />
              <dl className="service-definition-list">
                <div>
                  <dt>Architecture</dt>
                  <dd className="mono">{inspector.architecture}</dd>
                </div>
                <div>
                  <dt>Kubernetes</dt>
                  <dd className="mono">{inspector.kubelet_version}</dd>
                </div>
                <div>
                  <dt>Scheduling</dt>
                  <dd>{inspector.unschedulable ? 'Disabled (cordoned)' : 'Enabled'}</dd>
                </div>
                <div>
                  <dt>Allocatable CPU</dt>
                  <dd className="mono">{cpu(inspector.allocatable_cpu)} cores</dd>
                </div>
                <div>
                  <dt>Allocatable memory</dt>
                  <dd className="mono">{gib(memory(inspector.allocatable_memory))}</dd>
                </div>
                <div>
                  <dt>Pods / GPUs</dt>
                  <dd className="mono">
                    {inspector.pods} / {inspector.allocatable_gpu}
                  </dd>
                </div>
              </dl>
              {inspector.control_plane && (
                <Note>
                  Control plane scheduling is operator managed. Worker operations appear on worker
                  nodes.
                </Note>
              )}
            </div>
            <div className="dialog-footer">
              <Button variant="ghost" onClick={() => setSelectedNode('')}>
                Close
              </Button>
              {scope.identity.admin && !inspector.control_plane && (
                <Button
                  onClick={() => {
                    setAction({ node: inspector, action: 'cordon' })
                    setSelectedNode('')
                  }}
                >
                  {inspector.unschedulable ? 'Uncordon' : 'Cordon'}
                </Button>
              )}
              {canOpenHostTerminal(scope.identity, inspector.name) && (
                <Link
                  className="button button-primary"
                  to="/infrastructure/nodes/$node/terminal"
                  params={{ node: inspector.name }}
                >
                  <Icon name="terminal" size={14} />
                  Open terminal
                </Link>
              )}
            </div>
          </>
        )}
      </Dialog>
      <div className="infrastructure-notes">
        <Note>
          Allocatable values describe workload capacity. Service pages show observed pod CPU,
          memory, allocations, and events.
        </Note>
        <Note>
          Adding workers expands workload capacity. It does not make the control plane, database, or
          public ingress highly available.
        </Note>
      </div>
      {action && (
        <Suspense fallback={<Loading />}>
          <NodeAction
            node={action.node}
            action={action.action}
            onClose={() => setAction(null)}
            onChanged={probe}
          />
        </Suspense>
      )}
    </>
  )
}
