import { lazy, Suspense, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Badge, Tooltip } from '@hakopod/ui'
import { Icon } from './icons'
import { Button } from './ui/button'
import { Copy, Empty, ErrorState, Loading, Note } from './shared'
import { client, unwrap } from '../lib/client'
import { timestamp } from '../lib/api'
import type { components } from '../lib/api.generated'
const LiveLogs = lazy(() => import('./live-logs'))
type Query = components['schemas']['LogQuery']
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
  const entries = data?.entries.slice(0, 1000) || []
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
    <section className="log-explorer">
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
                <select
                  value={service}
                  onChange={(e) => {
                    setService(e.target.value)
                    setPod('')
                  }}
                >
                  {services.map((name) => (
                    <option key={name}>{name}</option>
                  ))}
                </select>
              </label>
              <label>
                Pod
                <select value={pod} onChange={(e) => setPod(e.target.value)}>
                  <option value="">All service pods</option>
                  {runtime.data?.pods.map((item) => (
                    <option key={item.name}>{item.name}</option>
                  ))}
                </select>
              </label>
              <label>
                Container
                <input
                  value={container}
                  onChange={(e) => setContainer(e.target.value)}
                  placeholder="app"
                  maxLength={63}
                />
              </label>
              <label>
                Time window
                <select value={since} onChange={(e) => setSince(Number(e.target.value))}>
                  <option value={900}>Last 15 minutes</option>
                  <option value={3600}>Last hour</option>
                  <option value={21600}>Last 6 hours</option>
                  <option value={86400}>Last 24 hours</option>
                </select>
              </label>
            </div>
            <div className="query-editor">
              <span className="query-prefix">WHERE</span>
              <textarea
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
                <input
                  type="checkbox"
                  checked={previous}
                  onChange={(e) => setPrevious(e.target.checked)}
                />
                Previous container
              </label>
              <label className="query-limit">
                Limit
                <select value={limit} onChange={(e) => setLimit(Number(e.target.value))}>
                  <option>100</option>
                  <option>500</option>
                  <option>1000</option>
                </select>
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
                <div
                  className="log-histogram"
                  role="img"
                  aria-label={`Log volume across ${histogram.length} time buckets; ${data.matched} matching entries in the sampled data`}
                >
                  {histogram.map((bar, index) => (
                    <Tooltip
                      key={`${bar.timestamp}-${index}`}
                      side="top"
                      content={`${timestamp(bar.timestamp)} · ${bar.count} entries`}
                    >
                      <div className="histogram-bucket" tabIndex={0}>
                        <i style={{ height: `${(bar.count / maximum) * 100}%` }} />
                        <span className="sr-only">
                          {timestamp(bar.timestamp)}: {bar.count}
                        </span>
                      </div>
                    </Tooltip>
                  ))}
                </div>
                <div className="histogram-axis">
                  <span>
                    {histogram[0] ? timestamp(histogram[0].timestamp) : 'No histogram returned'}
                  </span>
                  <span>{histogram.at(-1) ? timestamp(histogram.at(-1)!.timestamp) : ''}</span>
                </div>
                <div className="log-result-toolbar">
                  <span>
                    <strong>{data.matched}</strong> matches · {data.scanned} sampled lines ·{' '}
                    {data.pods} pods
                  </span>
                  {data.truncated && <Badge tone="warning">Truncated</Badge>}
                  <span className="form-spacer" />
                  <label className="checkbox-row">
                    <input
                      type="checkbox"
                      checked={wrap}
                      onChange={(e) => setWrap(e.target.checked)}
                    />
                    Wrap
                  </label>
                  <Copy value={entries.map((entry) => entry.message).join('\n')} label="Copy" />
                  <Button size="sm" variant="ghost" onClick={exportLogs} disabled={!entries.length}>
                    <Icon name="external" size={13} />
                    Export
                  </Button>
                </div>
                {data.warnings.map((warning) => (
                  <Note key={warning}>{warning}</Note>
                ))}
                <div className={`log-results ${wrap ? 'log-wrap' : ''}`} role="log" aria-live="off">
                  {entries.length ? (
                    entries.map((entry, index) => (
                      <details
                        className="log-entry"
                        key={`${entry.timestamp}-${entry.pod}-${index}`}
                      >
                        <summary>
                          <time>
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
                          <Icon name="down" size={12} />
                        </summary>
                        <div className="log-entry-detail">
                          <dl>
                            <div>
                              <dt>Time</dt>
                              <dd>{entry.timestamp || 'Unavailable'}</dd>
                            </div>
                            <div>
                              <dt>Source</dt>
                              <dd>
                                {entry.service} / {entry.pod} / {entry.container}
                              </dd>
                            </div>
                          </dl>
                          <pre>{entry.message}</pre>
                          {Object.keys(entry.fields).length > 0 && (
                            <pre>{JSON.stringify(entry.fields, null, 2)}</pre>
                          )}
                          <Copy value={JSON.stringify(entry, null, 2)} label="Copy entry" />
                        </div>
                      </details>
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
    </section>
  )
}
