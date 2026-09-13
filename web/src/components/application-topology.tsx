import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Badge, Card } from './ui/surfaces'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import type { Application } from '../lib/types'
import { Icon } from './icons'
import { HeadingHelp, Copy, Status, Note } from './shared'
export default function ApplicationTopology({ application: app }: { application: Application }) {
  const names = Object.keys(app.spec.services).slice(0, 32)
  const [selected, setSelected] = useState(names[0] || '')
  const name = names.includes(selected) ? selected : names[0]
  const service = app.spec.services[name]
  const observed = app.observed?.services?.find((item) => item.name === name)
  const positions = new Map(
    names.map((name, index) => [
      name,
      { x: 32 + (index % 3) * 260, y: 52 + Math.floor(index / 3) * 156 },
    ]),
  )
  const height = Math.max(280, Math.ceil(names.length / 3) * 156 + 65)
  const edges = names.flatMap((name) =>
    (app.spec.services[name].depends_on || [])
      .filter((dependency) => positions.has(dependency))
      .map((dependency) => ({ from: name, to: dependency })),
  )
  return (
    <div className="ops-topology">
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>Service topology</h2>
            <HeadingHelp title="Service topology">
              Applied services and declared readiness dependencies. Select a service to inspect it.
            </HeadingHelp>
          </div>
        </div>
        <Badge>
          {names.length} services · {edges.length} dependencies
        </Badge>
      </div>
      <div className="topology-layout">
        <div className="topology-viewport">
          <div className="topology-canvas" style={{ height, width: 810 }}>
            <svg className="topology-edges" width="810" height={height} aria-hidden="true">
              <defs>
                <marker
                  id="dependency-arrow"
                  viewBox="0 0 10 10"
                  refX="9"
                  refY="5"
                  markerWidth="5"
                  markerHeight="5"
                  orient="auto-start-reverse"
                >
                  <path d="M 0 0 L 10 5 L 0 10 z" fill="currentColor" />
                </marker>
              </defs>
              {edges.map(({ from, to }) => {
                const a = positions.get(from)!
                const b = positions.get(to)!
                const sameRow = a.y === b.y
                return (
                  <path
                    key={`${from}-${to}`}
                    className={from === name || to === name ? 'edge-selected' : ''}
                    d={
                      sameRow
                        ? `M${a.x + 104} ${a.y + 101} C${a.x + 104} ${a.y + 146} ${b.x + 104} ${b.y + 146} ${b.x + 104} ${b.y + 101}`
                        : `M${a.x + 104} ${a.y + 101} C${a.x + 104} ${a.y + 135} ${b.x + 104} ${b.y - 30} ${b.x + 104} ${b.y}`
                    }
                    markerEnd="url(#dependency-arrow)"
                  />
                )
              })}
            </svg>
            {names.map((item) => {
              const at = positions.get(item)!
              const config = app.spec.services[item]
              const state = app.observed?.services?.find((entry) => entry.name === item)
              return (
                <button
                  type="button"
                  key={item}
                  className={`topology-node interactive ${name === item ? 'selected hatch' : ''}`}
                  style={{ left: at.x, top: at.y }}
                  aria-pressed={name === item}
                  onClick={() => setSelected(item)}
                >
                  <Brackets />
                  <Status value={state?.status || 'not observed'} small />
                  <div>
                    <Icon
                      name={config.public ? 'globe' : config.port ? 'box' : 'terminal'}
                      size={18}
                    />
                    <strong>{item}</strong>
                    <Icon name="chevron" size={13} />
                  </div>
                  <small>
                    {state
                      ? `${state.ready}/${state.desired} ready`
                      : `${config.replicas || 1} desired`}{' '}
                    · {config.public ? 'Public HTTP' : config.port ? 'Private' : 'Worker'}
                    {config.volume ? ' · Volume' : ''}
                  </small>
                </button>
              )
            })}
            <div className="topology-legend">
              <span className="dependency-line" />
              Readiness dependency · no traffic rate inferred
            </div>
          </div>
        </div>
        {service && (
          <Card className="topology-inspector inspector-card">
            <div className="inspector-heading">
              <Status value={observed?.status || 'not observed'} small />
              <h2>{name}</h2>
              <Copy value={name} label="Copy service slug" />
            </div>
            {observed?.message && <p className="inspector-summary">{observed.message}</p>}
            <div className="inspector-facts">
              <div>
                <span>Replicas</span>
                <strong>
                  {observed ? `${observed.ready}/${observed.desired}` : 'Not observed'}
                </strong>
              </div>
              <div>
                <span>Profile</span>
                <strong>{service.size || 'small'}</strong>
              </div>
              <div>
                <span>Port</span>
                <strong>{service.port || 'None'}</strong>
              </div>
              <div>
                <span>Exposure</span>
                <strong>{service.public ? 'Public' : 'Private'}</strong>
              </div>
            </div>
            <dl className="topology-details">
              <div>
                <dt>Image</dt>
                <dd>
                  <code>{observed?.image || service.image}</code>
                  <Copy value={observed?.image || service.image} label="Copy image" />
                </dd>
              </div>
              <div>
                <dt>Depends on</dt>
                <dd>{service.depends_on?.join(', ') || 'No declared dependencies'}</dd>
              </div>
              <div>
                <dt>Networks</dt>
                <dd>{service.networks?.join(', ') || 'default'}</dd>
              </div>
              {service.volume && (
                <div>
                  <dt>Persistent volume</dt>
                  <dd>
                    {service.volume.size_gib} GiB · {service.volume.mount_path}
                  </dd>
                </div>
              )}
            </dl>
            <div className="inspector-actions">
              <Link
                to="/applications/$applicationId"
                params={{ applicationId: app.id }}
                search={{ service: name }}
                className="button button-secondary"
              >
                Inspect service
                <Icon name="arrow" size={14} />
              </Link>
              <Link
                to="/applications/$applicationId"
                params={{ applicationId: app.id }}
                search={{ service: name, tab: 'logs' }}
                className="button button-secondary"
              >
                Logs
              </Link>
              <Link
                to="/applications/$applicationId"
                params={{ applicationId: app.id }}
                search={{ service: name, tab: 'terminal' }}
                className="button button-secondary"
              >
                Terminal
              </Link>
            </div>
          </Card>
        )}
      </div>
      {Object.keys(app.spec.services).length > 32 && (
        <Note>
          The topology displays the first 32 services. Use the services list for the complete
          application.
        </Note>
      )}
    </div>
  )
}
