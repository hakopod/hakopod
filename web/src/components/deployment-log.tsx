import { useEffect, useRef, useState } from 'react'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import type { components } from '../lib/api.generated'
import { activeDeployment } from '../lib/api'
import { Icon } from './icons'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Copy, Status } from './shared'

type Event = components['schemas']['Event']
type PhaseStatus = 'succeeded' | 'failed' | 'running' | 'incomplete'
export type Phase = {
  id: string
  label: string
  service: string
  status: PhaseStatus
  start: string
  end: string
  events: Event[]
  error?: Event
}

// The API retains every event; the page renders the latest 200 like the previous event list.
export const retainedEvents = 200
const failure = /fail|error/
const finished = ['ready', 'completed', 'succeeded']

export function deploymentPhases(
  release: Pick<
    components['schemas']['Deployment'],
    'status' | 'created_at' | 'revision' | 'error'
  >,
  events: Event[],
): Phase[] {
  const active = activeDeployment(release.status)
  const accepted: Event = {
    id: -1,
    time: release.created_at,
    type: 'accepted',
    service: '',
    message: `Revision ${release.revision} persisted by the management API.`,
  }
  const groups = new Map<string, Event[]>([['', [accepted]]])
  for (const event of events) {
    const list = groups.get(event.service) || []
    list.push(event)
    groups.set(event.service, list)
  }
  const phases = [...groups].map(([service, list]): Phase => {
    const error = [...list].reverse().find((event) => failure.test(event.type))
    const last = list[list.length - 1]
    const status: PhaseStatus = service
      ? error && !finished.includes(last.type)
        ? 'failed'
        : finished.includes(last.type)
          ? 'succeeded'
          : active
            ? 'running'
            : 'incomplete'
      : release.status === 'failed'
        ? 'failed'
        : release.status === 'succeeded'
          ? 'succeeded'
          : active
            ? 'running'
            : 'incomplete'
    return {
      id: service ? `service-${service}` : 'release',
      label: service || 'Release',
      service,
      status,
      start: list[0].time,
      end: last.time,
      events: list,
      error:
        status !== 'failed'
          ? undefined
          : error ||
            (service || !release.error
              ? undefined
              : { ...accepted, id: -2, type: 'failed', message: release.error }),
    }
  })
  // The release phase stays first; services follow the order they started in.
  return [phases[0], ...phases.slice(1).sort((a, b) => Date.parse(a.start) - Date.parse(b.start))]
}

// A routine successful phase starts collapsed; the first failure and running work start open.
export function initiallyOpen(phases: Phase[]) {
  const failed = phases.filter((phase) => phase.status === 'failed')
  // A failed service explains the release failure better than the release summary.
  const primary = failed.find((phase) => phase.service) || failed[0]
  return Object.fromEntries(
    phases.map((phase) => [phase.id, phase === primary || phase.status === 'running']),
  )
}

export function duration(start: string, end: string) {
  const seconds = Math.round((Date.parse(end) - Date.parse(start)) / 1000)
  if (!Number.isFinite(seconds) || seconds < 0) return ''
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  return minutes < 60
    ? `${minutes}m ${seconds % 60}s`
    : `${Math.floor(minutes / 60)}h ${minutes % 60}m`
}

const clock = (value: string) => {
  const time = new Date(value)
  return Number.isFinite(time.getTime())
    ? time.toLocaleTimeString(undefined, { hour12: false })
    : '—'
}
const line = (event: Event) =>
  `${event.time} ${event.type} ${event.service || ''} ${event.message}`.replace(/ {2,}/g, ' ')
const statusLabel: Record<PhaseStatus, string> = {
  succeeded: 'Succeeded',
  failed: 'Failed',
  running: 'Running',
  incomplete: 'Not completed',
}

export function DeploymentLog({ release }: { release: components['schemas']['Deployment'] }) {
  const all = release.events || []
  const events = all.slice(-retainedEvents)
  const phases = deploymentPhases(release, events)
  const active = activeDeployment(release.status)
  // Only the reader's own toggles are kept; untouched phases follow their current status.
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const [search, setSearch] = useState('')
  const [wrap, setWrap] = useState(true)
  const [times, setTimes] = useState(true)
  const [follow, setFollow] = useState(active)
  const [current, setCurrent] = useState(0)
  const view = useRef<HTMLDivElement>(null)
  const query = search.trim().toLowerCase()
  const matches = (event: Event) =>
    !query || `${event.type} ${event.service} ${event.message}`.toLowerCase().includes(query)
  const found = query ? phases.reduce((n, phase) => n + phase.events.filter(matches).length, 0) : 0
  const defaults = initiallyOpen(phases)
  const isOpen = (phase: Phase) =>
    (query && phase.events.some(matches)) || (open[phase.id] ?? defaults[phase.id])
  useEffect(() => {
    // Follow only scrolls the log itself, never the page, and only while the reader has not scrolled away.
    if (follow && view.current) view.current.scrollTop = view.current.scrollHeight
  }, [follow, events.length])
  const goTo = (index: number) => {
    const phase = phases[index]
    if (!phase) return
    setCurrent(index)
    setFollow(false)
    setOpen((value) => ({ ...value, [phase.id]: true }))
    const header = view.current?.querySelector<HTMLElement>(`[data-phase="${phase.id}"]`)
    if (header && view.current) {
      view.current.scrollTop = header.offsetTop
      header.querySelector('button')?.focus({ preventScroll: true })
    }
  }
  const failedIndex = phases.findIndex((phase) => phase.status === 'failed' && defaults[phase.id])
  const download = () => {
    const url = URL.createObjectURL(
      new Blob([all.map(line).join('\n')], { type: 'text/plain;charset=utf-8' }),
    )
    const link = document.createElement('a')
    link.href = url
    link.download = `hakopod-deployment-r${release.revision}-events.txt`
    link.click()
    URL.revokeObjectURL(url)
  }
  return (
    <section className="inspect-log" aria-label="Deployment log">
      <Brackets />
      <div className="inspect-log-toolbar">
        <label className="inspect-log-search">
          <Icon name="search" size={15} />
          <span className="sr-only">Search deployment log</span>
          <Input
            type="search"
            value={search}
            placeholder="Search events"
            onChange={(event) => setSearch(event.target.value)}
          />
        </label>
        {query && (
          <span className="inspect-log-count" role="status">
            {found} {found === 1 ? 'match' : 'matches'}
          </span>
        )}
        <span className="form-spacer" />
        <label className="checkbox-row">
          <Input type="checkbox" checked={wrap} onChange={(e) => setWrap(e.target.checked)} />
          Wrap
        </label>
        <label className="checkbox-row">
          <Input type="checkbox" checked={times} onChange={(e) => setTimes(e.target.checked)} />
          Timestamps
        </label>
        <div className="inspect-log-nav" role="group" aria-label="Phase navigation">
          <Button
            size="icon"
            variant="ghost"
            aria-label="Previous phase"
            disabled={current <= 0}
            onClick={() => goTo(current - 1)}
          >
            <Icon name="chevron" size={15} className="rotate-180" />
          </Button>
          <Button
            size="icon"
            variant="ghost"
            aria-label="Next phase"
            disabled={current >= phases.length - 1}
            onClick={() => goTo(current + 1)}
          >
            <Icon name="chevron" size={15} />
          </Button>
          {failedIndex >= 0 && (
            <Button size="sm" variant="ghost" onClick={() => goTo(failedIndex)}>
              <Icon name="alert" size={14} />
              Go to failure
            </Button>
          )}
        </div>
        {active && (
          <Button
            size="sm"
            variant="ghost"
            aria-pressed={follow}
            onClick={() => setFollow(!follow)}
          >
            <Icon name={follow ? 'pause' : 'play'} size={14} />
            {follow ? 'Pause follow' : 'Follow'}
          </Button>
        )}
        <Copy value={all.map(line).join('\n')} label="Copy events" />
        <Button size="sm" variant="ghost" disabled={!all.length} onClick={download}>
          Download
        </Button>
      </div>
      <div
        ref={view}
        className={`inspect-log-view ${wrap ? 'inspect-log-wrap' : ''} ${times ? '' : 'inspect-log-no-time'}`}
        role="log"
        aria-label="Recorded deployment events"
        aria-live="off"
        tabIndex={0}
        onScroll={(event) => {
          const node = event.currentTarget
          if (follow && node.scrollHeight - node.scrollTop - node.clientHeight > 24)
            setFollow(false)
        }}
      >
        {all.length > events.length && (
          <p className="inspect-log-dropped">
            {all.length - events.length} earlier events are not shown. Download includes all{' '}
            {all.length}.
          </p>
        )}
        {phases.map((phase) => {
          const expanded = isOpen(phase)
          const rows = phase.events.filter(matches)
          const span = phase.status === 'running' ? '' : duration(phase.start, phase.end)
          return (
            <section
              key={phase.id}
              className="inspect-phase"
              data-phase={phase.id}
              data-status={phase.status}
            >
              <h3>
                <button
                  type="button"
                  className="inspect-phase-header"
                  aria-expanded={expanded}
                  aria-controls={`phase-${phase.id}`}
                  onClick={() => setOpen((value) => ({ ...value, [phase.id]: !expanded }))}
                >
                  <Icon name={expanded ? 'down' : 'chevron'} size={14} />
                  {phase.service ? <code>{phase.label}</code> : <span>{phase.label}</span>}
                  <Status value={statusLabel[phase.status]} small />
                  <span className="inspect-phase-meta">
                    {phase.events.length} {phase.events.length === 1 ? 'event' : 'events'}
                    {span && ` · ${span}`}
                  </span>
                </button>
              </h3>
              {phase.error && (
                <div className="inspect-phase-error">
                  <strong>{phase.service ? `${phase.service} failed` : 'Deployment failed'}</strong>
                  <p>{phase.error.message}</p>
                </div>
              )}
              <div id={`phase-${phase.id}`} hidden={!expanded}>
                {rows.map((event, index) => (
                  <div
                    className="inspect-log-row"
                    key={`${event.id}-${index}`}
                    data-error={failure.test(event.type) || undefined}
                  >
                    <time dateTime={event.time} title={event.time}>
                      {clock(event.time)}
                    </time>
                    <span>
                      <span className="inspect-log-type">{event.type.replaceAll('_', ' ')}</span>
                      {event.message}
                    </span>
                  </div>
                ))}
                {query && !rows.length && <p className="inspect-log-empty">No matching events.</p>}
              </div>
            </section>
          )
        })}
        {!events.length && (
          <p className="inspect-log-empty">
            {active
              ? 'Waiting for the reconciler’s first event.'
              : 'No additional events were recorded.'}
          </p>
        )}
      </div>
      <div className="inspect-log-footer">
        <span>
          {active
            ? 'Observing the reconciler every 2.5 seconds'
            : 'Events retained with this release'}
        </span>
        <span>
          {phases.length} {phases.length === 1 ? 'phase' : 'phases'} · {all.length} events
        </span>
      </div>
    </section>
  )
}
