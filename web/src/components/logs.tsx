import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Textarea } from './ui/textarea'
import { lazy, Suspense, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Badge, Tooltip } from './ui/surfaces'
import { Dialog } from './ui/dialog'
import { Icon } from './icons'
import { Button } from './ui/button'
import { Copy, Empty, ErrorState, Loading, Note } from './shared'
import { client, unwrap } from '../lib/client'
import { timestamp } from '../lib/api'
import type { components } from '../lib/api.generated'
const LiveLogs = lazy(() => import('./live-logs'))
type Query = components['schemas']['LogQuery']
type LogEntry = components['schemas']['LogEntry']
const examples = [
  ['Errors', 'severity >= ERROR'],
  ['Timeouts', "message ILIKE '%timeout%'"],
  ['HTTP 5xx', 'json.status >= 500'],
  ['Exclude health checks', "NOT message ILIKE '%health%'"],
] as const
export function Logs({
  applicationId,
  services,
  initialService,
}: {
  applicationId: string
  services: string[]
  initialService?: string
}) {
  const [selected, setSelected] = useState<LogEntry | null>(null)
  const [range, setRange] = useState<{ from: number; to: number } | null>(null)
  const [mode, setMode] = useState<'explore' | 'live'>('explore')
  const [service, setService] = useState(initialService || services[0] || '')
  const [pod, setPod] = useState('')
  const [container, setContainer] = useState('app')
  const [draft, setDraft] = useState('')
  const [since, setSince] = useState(3600)
  const [limit, setLimit] = useState(500)
  const [previous, setPrevious] = useState(false)
  const [wrap, setWrap] = useState(false)
  const [query, setQuery] = useState<Query>({
    service,
    since_seconds: 3600,
    limit: 500,
    tail: 2000,
  })
  const runtime = useQuery({
    queryKey: ['log-pods', applicationId, service],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/services/{service}/runtime', {
          signal,
          params: { path: { id: applicationId, service } },
        }),
      ),
    enabled: Boolean(service) && mode === 'explore',
    gcTime: 0,
    staleTime: 30000,
  })
  const logs = useQuery({
    queryKey: ['log-query', applicationId, query],
    queryFn: ({ signal }) =>
      unwrap(
        client.POST('/applications/{id}/logs/query', {
          signal,
          params: { path: { id: applicationId } },
          body: query,
        }),
      ),
    enabled: Boolean(query.service) && mode === 'explore',
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false,
  })
  const run = () => {
    setRange(null)
    const next = {
      service,
      pod: pod || undefined,
      container: container || 'app',
      query: draft,
      since_seconds: since,
      limit,
      tail: 2000,
      previous,
    }
    if (JSON.stringify(next) === JSON.stringify(query)) void logs.refetch()
    else setQuery(next)
  }
  const data = logs.data
  const histogram = data?.histogram.slice(0, 120) || []
  const maximum = Math.max(1, ...histogram.map((bar) => bar.count))
  const entries = (data?.entries.slice(0, 1000) || []).filter(
    (entry) =>
      !range ||
      (Date.parse(entry.timestamp) >= range.from && Date.parse(entry.timestamp) < range.to),
  )
  const exportLogs = () => {
    const blob = new Blob([entries.map((entry) => JSON.stringify(entry)).join('\n')], {
      type: 'application/x-ndjson',
    })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'hakopod-logs.jsonl'
    a.click()
    URL.revokeObjectURL(url)
  }
  if (!services.length)
    return (
      <Empty
        icon="terminal"
        title="No services to inspect"
        description="Deploy a service to view its container output."
      />
    )
  return (
    <section className="log-explorer ops-logs">
      <div className="explorer-heading">
        <div>
          <Icon name="terminal" size={18} />
          <h2>Log explorer</h2>
          <Badge>CONTAINER OUTPUT</Badge>
        </div>
        <div className="view-switch" role="group" aria-label="Log mode">
          <Button
            size="sm"
            variant="ghost"
            aria-pressed={mode === 'explore'}
            onClick={() => setMode('explore')}
          >
            Query
          </Button>
          <Button
            size="sm"
            variant="ghost"
            aria-pressed={mode === 'live'}
            onClick={() => setMode('live')}
          >
            Live tail
          </Button>
        </div>
      </div>
      {mode === 'live' ? (
        <Suspense fallback={<Loading />}>
          <LiveLogs applicationId={applicationId} services={services} initialService={service} />
        </Suspense>
      ) : (
        <>
          <section className="log-volume-panel" aria-label="Log volume">
            <div className="log-volume-heading">
              <span>Log volume</span>
              <small>{logs.isFetching ? 'Querying…' : 'Returned time buckets'}</small>
            </div>
            {data && !logs.error ? (
              <>
                <div
                  className="log-histogram"
                  role="group"
                  aria-label={`Log volume across ${histogram.length} time buckets; ${data.matched} matching entries in the sampled data`}
                >
                  {histogram.map((bar, index) => (
                    <Tooltip
                      key={`${bar.timestamp}-${index}`}
                      side="top"
                      content={`${timestamp(bar.timestamp)} · ${bar.count} entries`}
                    >
                      <button
                        className="histogram-bucket"
                        type="button"
                        aria-label={`Inspect ${bar.count} entries from ${timestamp(bar.timestamp)}`}
                        aria-pressed={range?.from === Date.parse(bar.timestamp)}
                        onClick={() => {
                          const from = Date.parse(bar.timestamp)
                          const next = histogram[index + 1]?.timestamp
                          const previous = histogram[index - 1]?.timestamp
                          const step = previous ? from - Date.parse(previous) : 60000
                          setRange({ from, to: next ? Date.parse(next) : from + step })
                        }}
                      >
                        <i style={{ height: `${(bar.count / maximum) * 100}%` }} />
                        <span className="sr-only">
                          {timestamp(bar.timestamp)}: {bar.count}
                        </span>
                      </button>
                    </Tooltip>
                  ))}
                </div>
                <div className="histogram-axis">
                  <span>
                    {histogram[0] ? timestamp(histogram[0].timestamp) : 'No histogram returned'}
                  </span>
                  <span>{histogram.at(-1) ? timestamp(histogram.at(-1)!.timestamp) : ''}</span>
                </div>
                {range && (
                  <div className="ops-range-banner">
                    <span>
                      Showing loaded entries from {timestamp(new Date(range.from).toISOString())} to{' '}
                      {timestamp(new Date(range.to).toISOString())}
                    </span>
                    <Button size="sm" variant="ghost" onClick={() => setRange(null)}>
                      Clear time selection
                    </Button>
                  </div>
                )}
              </>
            ) : (
              <p className="log-volume-empty">
                {logs.isPending ? 'Waiting for query results.' : 'No histogram available.'}
              </p>
            )}
          </section>
          <form
            className="log-query-form"
            onSubmit={(e) => {
              e.preventDefault()
              run()
            }}
          >
            <div className="log-filter-row">
              <label>
                Service
                <SelectField
                  label="Service"
                  value={service}
                  onValueChange={(value) => {
                    setService(value)
                    setPod('')
                  }}
                  options={services.map((name) => ({ value: name, label: name }))}
                />
              </label>
              <label>
                Pod
                <SelectField
                  label="Pod"
                  value={pod}
                  onValueChange={setPod}
                  options={[
                    { value: '', label: 'All service pods' },
                    ...(runtime.data?.pods || []).map((item) => ({
                      value: item.name,
                      label: item.name,
                    })),
                  ]}
                />
              </label>
              <label>
                Container
                <Input
                  value={container}
                  onChange={(e) => setContainer(e.target.value)}
                  placeholder="app"
                  maxLength={63}
                />
              </label>
              <label>
                Time window
                <SelectField
                  label="Time window"
                  value={String(since)}
                  onValueChange={(value) => setSince(Number(value))}
                  options={[
                    { value: '900', label: 'Last 15 minutes' },
                    { value: '3600', label: 'Last hour' },
                    { value: '21600', label: 'Last 6 hours' },
                    { value: '86400', label: 'Last 24 hours' },
                  ]}
                />
              </label>
            </div>
            <div className="query-editor">
              <span className="query-prefix">WHERE</span>
              <Textarea
                aria-label="SQL-like log filter"
                placeholder="severity >= ERROR AND message ILIKE '%timeout%'"
                value={draft}
                maxLength={4096}
                rows={2}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => {
                  if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
                    e.preventDefault()
                    run()
                  }
                }}
              />
              <Button type="submit" variant="primary" disabled={logs.isFetching}>
                <Icon name="play" size={14} />
                {logs.isFetching ? 'Querying…' : 'Run query'}
              </Button>
            </div>
            <div className="query-examples">
              <span>Try</span>
              {examples.map(([name, value]) => (
                <button type="button" key={name} onClick={() => setDraft(value)}>
                  {name}
                </button>
              ))}
              <span className="form-spacer" />
              <label className="checkbox-row">
                <Input
                  type="checkbox"
                  checked={previous}
                  onChange={(e) => setPrevious(e.target.checked)}
                />
                Previous container
              </label>
              <label className="query-limit">
                Limit
                <SelectField
                  label="Limit"
                  compact
                  value={String(limit)}
                  onValueChange={(value) => setLimit(Number(value))}
                  options={['100', '500', '1000'].map((value) => ({ value, label: value }))}
                />
              </label>
            </div>
            <details className="query-reference">
              <summary>Filter syntax</summary>
              <p>
                Use fields <code>timestamp</code>, <code>pod</code>, <code>service</code>,{' '}
                <code>container</code>, <code>message</code>, <code>severity</code>, or{' '}
                <code>json.status</code>. Combine comparisons with <code>AND</code>, <code>OR</code>
                , <code>NOT</code>, and parentheses. <code>ILIKE</code> matches text without case.
                This filters sampled container output; it does not query a retained log database.
              </p>
            </details>
          </form>
          {logs.error ? (
            <ErrorState error={logs.error} retry={run} />
          ) : logs.isPending ? (
            <Loading rows={3} />
          ) : (
            data && (
              <>
                <div className="log-result-toolbar">
                  <span>
                    <strong>{range ? entries.length : data.matched}</strong>{' '}
                    {range ? 'visible entries' : 'matches'} · {data.scanned} sampled lines ·{' '}
                    {data.pods} pods
                  </span>
                  {data.truncated && <Badge tone="warning">Truncated</Badge>}
                  <span className="form-spacer" />
                  <label className="checkbox-row">
                    <Input
                      type="checkbox"
                      checked={wrap}
                      onChange={(e) => setWrap(e.target.checked)}
                    />
                    Wrap
                  </label>
                  <Copy value={entries.map((entry) => entry.message).join('\n')} label="Copy" />
                  <Button size="sm" variant="ghost" onClick={exportLogs} disabled={!entries.length}>
                    <Icon name="external" size={13} />
                    Download
                  </Button>
                </div>
                {data.warnings.map((warning) => (
                  <Note key={warning}>{warning}</Note>
                ))}
                <div className={`log-results ${wrap ? 'log-wrap' : ''}`} role="log" aria-live="off">
                  {entries.length ? (
                    entries.map((entry, index) => (
                      <button
                        type="button"
                        className="ops-log-line"
                        key={`${entry.timestamp}-${entry.pod}-${index}`}
                        onClick={() => setSelected(entry)}
                        aria-label={`Inspect ${entry.severity || 'default'} log from ${entry.pod} at ${entry.timestamp || 'unknown time'}`}
                      >
                        <time title={entry.timestamp}>
                          {entry.timestamp
                            ? new Date(entry.timestamp).toLocaleTimeString(undefined, {
                                hour12: false,
                              })
                            : '—'}
                        </time>
                        <span className={`log-severity severity-${entry.severity.toLowerCase()}`}>
                          {entry.severity || 'DEFAULT'}
                        </span>
                        <code className="log-pod" title={entry.pod}>
                          {entry.pod}
                        </code>
                        <span className="log-message">{entry.message}</span>
                        <Icon name="chevron" size={12} />
                      </button>
                    ))
                  ) : (
                    <Empty
                      icon="search"
                      title="No matching log entries"
                      description="Change your filter, container, or time window, then run the query again."
                    />
                  )}
                </div>
                <div className="log-footer">
                  <span>Queried {query.service} · Recent pod output</span>
                  <span>Up to 2,000 lines per sampled pod · {query.limit} result limit</span>
                </div>
              </>
            )
          )}
        </>
      )}
      <Dialog
        sheet
        open={Boolean(selected)}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
        title="Log entry"
        description="Structured fields returned by the selected container."
      >
        {selected && (
          <>
            <div className="dialog-body ops-log-inspector">
              <Badge>{selected.severity || 'DEFAULT'}</Badge>
              <dl className="service-definition-list">
                <div>
                  <dt>Timestamp</dt>
                  <dd className="mono">{selected.timestamp || 'Unavailable'}</dd>
                </div>
                <div>
                  <dt>Service</dt>
                  <dd>
                    <code>{selected.service}</code>
                    <Copy value={selected.service} />
                  </dd>
                </div>
                <div>
                  <dt>Pod</dt>
                  <dd className="break-text">
                    <code>{selected.pod}</code>
                    <Copy value={selected.pod} />
                  </dd>
                </div>
                <div>
                  <dt>Container</dt>
                  <dd>
                    <code>{selected.container}</code>
                  </dd>
                </div>
              </dl>
              <h3>Message</h3>
              <pre className="ops-inspector-code">{selected.message}</pre>
              {Object.keys(selected.fields).length > 0 && (
                <>
                  <h3>Structured fields</h3>
                  <pre className="ops-inspector-code">
                    {JSON.stringify(selected.fields, null, 2)}
                  </pre>
                </>
              )}
            </div>
            <div className="dialog-footer">
              <Button variant="ghost" onClick={() => setSelected(null)}>
                Close
              </Button>
              <Copy value={JSON.stringify(selected, null, 2)} label="Copy JSON" />
            </div>
          </>
        )}
      </Dialog>
    </section>
  )
}
