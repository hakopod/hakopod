import type { VirtualNetworkDetail } from '../lib/virtual-networks'
import { plural } from './create-flow'

// Network → segments → granted applications → connected services, drawn from the
// recorded grants and connections only. Granted applications without a connected
// service are dashed; a connection whose grant was removed is marked.

const slot = 64
const nodeHeight = 48
const column = { network: 0, segment: 220, application: 450, service: 700 }
const width = { network: 170, segment: 180, application: 200, service: 260 }
const viewWidth = column.service + width.service

const clip = (value: string, length: number) =>
  value.length > length ? `${value.slice(0, length - 1)}…` : value

type Row = {
  application: string
  granted: boolean
  services: VirtualNetworkDetail['connections']
}

export function NetworkTopology({ detail }: { detail: VirtualNetworkDetail }) {
  const segments = Object.entries(detail.spec.segments).map(([name, grant]) => {
    const connected = detail.connections.filter((item) => item.segment === name)
    const names = [
      ...grant.applications,
      ...new Set(
        connected
          .map((item) => item.application)
          .filter((application) => !grant.applications.includes(application)),
      ),
    ]
    const rows: Row[] = names.map((application) => ({
      application,
      granted: grant.applications.includes(application),
      services: connected.filter((item) => item.application === application),
    }))
    return { name, rows, connected: connected.length }
  })
  // Each service takes a slot; an application without services still takes one.
  let cursor = 0
  const laid = segments.map((segment) => {
    const top = cursor
    const rows = segment.rows.map((row) => {
      const rowTop = cursor
      cursor += Math.max(1, row.services.length)
      return { ...row, top: rowTop, slots: Math.max(1, row.services.length) }
    })
    if (!rows.length) cursor += 1
    return { ...segment, rows, top, slots: Math.max(1, cursor - top) }
  })
  const totalSlots = Math.max(1, cursor)
  const height = totalSlots * slot
  const middle = (top: number, slots: number) => (top + slots / 2) * slot
  const edge = (x1: number, y1: number, x2: number, y2: number) => {
    const bend = (x2 - x1) / 2
    return `M${x1} ${y1} C${x1 + bend} ${y1} ${x2 - bend} ${y2} ${x2} ${y2}`
  }
  const networkY = height / 2
  const summary = `${detail.network.name}: ${laid
    .map(
      (segment) =>
        `segment ${segment.name} allows ${segment.rows.filter((row) => row.granted).length} applications and has ${plural(segment.connected, 'connected service')}`,
    )
    .join('; ')}.`
  return (
    <figure className="network-topology">
      <div className="network-topology-scroll" tabIndex={0} aria-label="Network topology, scrollable">
        <svg
          viewBox={`-2 -2 ${viewWidth + 4} ${height + 4}`}
          width={viewWidth + 4}
          height={height + 4}
          role="img"
          aria-label={summary}
        >
          {laid.map((segment) => {
            const segmentY = middle(segment.top, segment.slots)
            return (
              <g key={segment.name}>
                <path
                  className="network-topology-edge"
                  data-state={segment.connected ? 'connected' : undefined}
                  d={edge(column.network + width.network, networkY, column.segment, segmentY)}
                />
                {segment.rows.map((row) => {
                  const rowY = middle(row.top, row.slots)
                  return (
                    <g key={row.application}>
                      <path
                        className="network-topology-edge"
                        data-state={row.services.length ? 'connected' : 'granted'}
                        d={edge(column.segment + width.segment, segmentY, column.application, rowY)}
                      />
                      {row.services.map((service, index) => {
                        const y = (row.top + index + 0.5) * slot
                        return (
                          <path
                            key={service.service}
                            className="network-topology-edge"
                            data-state="connected"
                            d={edge(
                              column.application + width.application,
                              rowY,
                              column.service,
                              y,
                            )}
                          />
                        )
                      })}
                    </g>
                  )
                })}
              </g>
            )
          })}
          <Node
            x={column.network}
            y={networkY}
            w={width.network}
            kind="network"
            title={detail.network.name}
            detail={plural(laid.length, 'segment')}
          />
          {laid.map((segment) => (
            <g key={segment.name}>
              <Node
                x={column.segment}
                y={middle(segment.top, segment.slots)}
                w={width.segment}
                kind="segment"
                title={segment.name}
                detail={
                  segment.rows.length
                    ? `${plural(segment.connected, 'service')} connected`
                    : 'No application granted'
                }
              />
              {segment.rows.map((row) => (
                <g key={row.application}>
                  <Node
                    x={column.application}
                    y={middle(row.top, row.slots)}
                    w={width.application}
                    kind="application"
                    state={!row.granted ? 'ungranted' : row.services.length ? undefined : 'granted'}
                    title={row.application}
                    detail={
                      !row.granted
                        ? 'Grant removed'
                        : row.services.length
                          ? plural(row.services.length, 'service')
                          : 'Allowed · not connected'
                    }
                  />
                  {row.services.map((service, index) => (
                    <Node
                      key={service.service}
                      x={column.service}
                      y={(row.top + index + 0.5) * slot}
                      w={width.service}
                      kind="service"
                      title={service.service}
                      detail={`${service.address}${service.ports[0] ? `:${service.ports[0].port}` : ''}`}
                      mono
                    />
                  ))}
                </g>
              ))}
            </g>
          ))}
        </svg>
      </div>
      <figcaption className="network-topology-legend">
        <span>
          <i data-state="connected" aria-hidden="true" /> Connected
        </span>
        <span>
          <i data-state="granted" aria-hidden="true" /> Allowed, not connected
        </span>
        <span>Services under the same segment can reach each other.</span>
      </figcaption>
    </figure>
  )
}

function Node({
  x,
  y,
  w,
  kind,
  state,
  title,
  detail,
  mono,
}: {
  x: number
  y: number
  w: number
  kind: 'network' | 'segment' | 'application' | 'service'
  state?: 'granted' | 'ungranted'
  title: string
  detail: string
  mono?: boolean
}) {
  const chars = Math.floor((w - 24) / 7.4)
  return (
    <g className="network-topology-node" data-kind={kind} data-state={state}>
      <title>{`${title} · ${detail}`}</title>
      <rect x={x} y={y - nodeHeight / 2} width={w} height={nodeHeight} />
      <text x={x + 12} y={y - 4} className="network-topology-title">
        {clip(title, chars)}
      </text>
      <text
        x={x + 12}
        y={y + 13}
        className={`network-topology-detail${mono ? ' network-topology-mono' : ''}`}
      >
        {clip(detail, chars + 4)}
      </text>
    </g>
  )
}
