import { lazy, Suspense, useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { useScope } from '../lib/scope'
import type { Node } from '../lib/types'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { Icon } from '../components/icons'
import { Button } from '../components/ui/button'
import { Empty, ErrorState, Loading, Note, PageHeader, Status } from '../components/shared'

const NodeEnrollments = lazy(() => import('../components/node-controls'))
const NodeAction = lazy(() =>
  import('../components/node-controls').then((m) => ({ default: m.NodeAction })),
)
const RegistrySettings = lazy(() => import('../components/registry-settings'))
const IssuerSettings = lazy(() => import('../components/tls-settings'))
const ProxySettings = lazy(() => import('../components/proxy-settings'))

export const Route = createFileRoute('/infrastructure')({ component: Infrastructure })
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
  const scope = useScope()
  return (
    <>
      <PageHeader
        eyebrow="OPERATOR / INFRASTRUCTURE"
        title="Your cloud starts here."
        description="Machines, private registries, and the ingress behind your applications."
      />
      <Tabs.Root defaultValue="nodes">
        <Tabs.List className="tab-list">
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
      </Tabs.Root>
    </>
  )
}
function Nodes() {
  const scope = useScope()
  const [action, setAction] = useState<{ node: Node; action: 'cordon' | 'drain' } | null>(null)
  const [search, setSearch] = useState('')
  const nodes = useQuery({
    queryKey: ['nodes'],
    queryFn: ({ signal }) => unwrap(client.GET('/nodes', { signal })),
    refetchInterval: 30000,
  })
  const items = nodes.data?.items || []
  const filtered = items.filter((node) => node.name.includes(search))
  const totalCPU = items.reduce((sum, node) => sum + cpu(node.allocatable_cpu), 0)
  const totalMemory = items.reduce((sum, node) => sum + memory(node.allocatable_memory), 0)
  return (
    <>
      <div className="overview-stats">
        <div className="overview-stat">
          <div>
            <span>Ready nodes</span>
            <strong>
              {nodes.data ? items.filter((node) => node.ready).length : '—'}
              <small> / {nodes.data ? items.length : '—'}</small>
            </strong>
          </div>
          <span className="stat-icon stat-green">
            <Icon name="server" />
          </span>
          <p>Kubernetes readiness condition</p>
        </div>
        <div className="overview-stat">
          <div>
            <span>Allocatable CPU</span>
            <strong>
              {nodes.data && Number.isFinite(totalCPU)
                ? totalCPU.toLocaleString(undefined, { maximumFractionDigits: 2 })
                : '—'}
              <small> cores</small>
            </strong>
          </div>
          <span className="stat-icon">
            <Icon name="activity" />
          </span>
          <p>Capacity available to workloads</p>
        </div>
        <div className="overview-stat">
          <div>
            <span>Allocatable memory</span>
            <strong>{nodes.data ? gib(totalMemory) : '—'}</strong>
          </div>
          <span className="stat-icon">
            <Icon name="grid" />
          </span>
          <p>After system reservations</p>
        </div>
      </div>
      <div className="section-toolbar">
        <div>
          <h2>
            Cluster nodes <span className="count-badge">{items.length}</span>
          </h2>
          <p>
            {nodes.dataUpdatedAt
              ? `Last refreshed ${timestamp(new Date(nodes.dataUpdatedAt).toISOString())}`
              : 'Waiting for cluster observation'}
          </p>
        </div>
        <Button onClick={() => void nodes.refetch()}>
          <Icon name="refresh" size={15} />
          Refresh nodes
        </Button>
        <div className="search-input">
          <Icon name="search" size={16} />
          <input
            aria-label="Search nodes"
            placeholder="Find a node…"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
        </div>
      </div>
      {nodes.isPending ? (
        <Loading />
      ) : nodes.error ? (
        <ErrorState error={nodes.error} retry={() => void nodes.refetch()} />
      ) : !items.length ? (
        <Empty
          icon="server"
          title="No nodes reported"
          description="Check the management API’s Kubernetes context and cluster connectivity. Nodes will appear when the cluster returns them."
        />
      ) : (
        <div className="table-container">
          <table className="node-table">
            <thead>
              <tr>
                <th>Node</th>
                <th>Status</th>
                <th>Allocatable CPU</th>
                <th>Allocatable memory</th>
                <th>Current usage</th>
                <th>GPUs</th>
                <th>Pods</th>
                <th>Kubernetes</th>
                {scope.identity.admin && <th>Operations</th>}
              </tr>
            </thead>
            <tbody>
              {filtered.map((node) => (
                <tr key={node.name}>
                  <td>
                    <div className="node-name">
                      <div className="node-icon">
                        <Icon name="server" size={21} />
                      </div>
                      <div>
                        <strong>{node.name}</strong>
                        <span>
                          {node.architecture}
                          {node.control_plane ? ' · Control plane' : ' · Worker'}
                          {node.unschedulable ? ' · Scheduling disabled' : ' · Schedulable'}
                        </span>
                      </div>
                    </div>
                  </td>
                  <td>
                    <Status value={node.ready ? 'ready' : 'not ready'} small />
                  </td>
                  <td>
                    <strong className="mono">
                      {cpu(node.allocatable_cpu).toLocaleString(undefined, {
                        maximumFractionDigits: 2,
                      })}
                    </strong>
                    <span className="muted-text"> cores</span>
                  </td>
                  <td>
                    <strong className="mono">{gib(memory(node.allocatable_memory))}</strong>
                  </td>
                  <td>
                    {node.metrics.available ? (
                      <div className="node-usage">
                        <strong>
                          {node.metrics.cpu_millicores === undefined
                            ? 'CPU unavailable'
                            : `${node.metrics.cpu_millicores.toFixed(0)} mCPU`}
                        </strong>
                        <span>
                          {node.metrics.memory_bytes === undefined
                            ? 'Memory unavailable'
                            : gib(node.metrics.memory_bytes)}
                        </span>
                        <small>{timestamp(node.metrics.sampled_at)}</small>
                      </div>
                    ) : (
                      <span className="muted-text" title={node.metrics.reason}>
                        Unavailable
                      </span>
                    )}
                  </td>
                  <td className="mono">{node.allocatable_gpu}</td>
                  <td className="mono">{node.pods}</td>
                  <td>
                    <code className="version-label">{node.kubelet_version}</code>
                  </td>
                  {scope.identity.admin && (
                    <td>
                      {node.control_plane ? (
                        <span className="field-help">Operator managed</span>
                      ) : (
                        <div className="toolbar-actions">
                          <Button
                            size="sm"
                            onClick={() =>
                              setAction({ node: structuredClone(node), action: 'cordon' })
                            }
                          >
                            {node.unschedulable ? 'Uncordon' : 'Cordon'}
                          </Button>
                          <Button
                            size="sm"
                            onClick={() =>
                              setAction({ node: structuredClone(node), action: 'drain' })
                            }
                          >
                            Drain
                          </Button>
                        </div>
                      )}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
          {!filtered.length && (
            <Empty
              icon="search"
              title="No matching nodes"
              description="Try a different node name."
            />
          )}
        </div>
      )}
      <div className="infrastructure-notes">
        <Note>
          Allocatable values describe workload capacity. Inspect individual services for observed
          pod CPU, memory, allocations, and events.
        </Note>
        <Note>
          Adding worker nodes expands workload capacity. It does not make the Kubernetes control
          plane, database, or public ingress highly available.
        </Note>
      </div>
      {action && (
        <Suspense fallback={<Loading />}>
          <NodeAction
            node={action.node}
            action={action.action}
            onClose={() => setAction(null)}
            onChanged={() => void nodes.refetch()}
          />
        </Suspense>
      )}
    </>
  )
}
