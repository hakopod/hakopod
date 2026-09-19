import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { timestamp } from '../lib/api'
import { useProjects } from '../lib/projects'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Dialog } from './ui/dialog'
import { SelectField } from './ui/select'
import { Copy, Empty, ErrorState, HeadingHelp, Loading, Status } from './shared'

type Entry = components['schemas']['RequestEntry']
export type RequestFilters = {
  project?: string
  environment?: string
  application_id?: string
  service?: string
  search?: string
  method?: string
  status?: number
  since_seconds?: number
}
export function requestSearch(search: Record<string, unknown>): RequestFilters {
  const out: RequestFilters = {}
  for (const key of [
    'project',
    'environment',
    'application_id',
    'service',
    'search',
    'method',
  ] as const)
    if (typeof search[key] === 'string') out[key] = search[key]
  for (const key of ['status', 'since_seconds'] as const)
    if (Number.isFinite(Number(search[key]))) out[key] = Number(search[key])
  return out
}
const ms = (value: number) => (value < 0 ? 'Not measured' : `${value.toLocaleString()} ms`)
function HTTPStatus({ value }: { value: number }) {
  return (
    <span className={value >= 400 ? 'font-mono text-[var(--error-text)]' : 'font-mono'}>
      {value || 'No response'}
    </span>
  )
}

export function Requests({
  initial = {},
  applicationId,
  service,
}: {
  initial?: RequestFilters
  applicationId?: string
  service?: string
}) {
  const [filters, setFilters] = useState<RequestFilters>(initial)
  const [search, setSearch] = useState(initial.search || '')
  const [page, setPage] = useState({ cursor: '', previous: [] as string[] })
  const [live, setLive] = useState(true)
  const [selected, setSelected] = useState<Entry | null>(null)
  const projects = useProjects()
  const query = {
    ...filters,
    application_id: applicationId || filters.application_id,
    service: service || filters.service,
    since_seconds: filters.since_seconds || 3600,
    limit: 50,
    cursor: page.cursor || undefined,
  }
  const result = useQuery({
    queryKey: ['requests', query],
    queryFn: ({ signal }) => unwrap(client.GET('/requests', { signal, params: { query } })),
    refetchInterval: live && !page.cursor ? 5000 : false,
    refetchIntervalInBackground: false,
    gcTime: 0,
    retry: false,
  })
  const change = (next: RequestFilters) => {
    setFilters(next)
    setPage({ cursor: '', previous: [] })
  }
  const entries = result.data?.items || []
  return (
    <div className="flex min-w-0 flex-col gap-4">
      {applicationId && service && (
        <RequestRouting
          applicationId={applicationId}
          service={service}
          onHost={(host) => {
            setSearch(host)
            change({ ...filters, search: host })
          }}
        />
      )}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <h2 className="text-sm font-semibold">Request log</h2>
          <HeadingHelp title="Request log">
            Completed HTTP requests observed by the managed ingress. Internal traffic, raw TCP, and
            requests that bypass this ingress are not captured. Queries, headers and bodies are
            omitted. Paths may still contain personal data.
          </HeadingHelp>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="secondary" aria-pressed={live} onClick={() => setLive(!live)}>
            {live ? 'Pause updates' : 'Resume updates'}
          </Button>
          <Button size="sm" disabled={result.isFetching} onClick={() => void result.refetch()}>
            Refresh
          </Button>
        </div>
      </div>
      <form
        className="grid min-w-0 grid-cols-1 items-end gap-3 sm:grid-cols-2 xl:grid-cols-5"
        onSubmit={(e) => {
          e.preventDefault()
          change({ ...filters, search: search.trim() || undefined })
        }}
      >
        {!applicationId && (
          <SelectField
            label="Project"
            value={filters.project || ''}
            onValueChange={(project) =>
              change({
                ...filters,
                project: project || undefined,
                environment: undefined,
                application_id: undefined,
                service: undefined,
              })
            }
            options={[
              { value: '', label: 'All accessible projects' },
              ...(filters.project && !projects.data?.items.some((p) => p.name === filters.project)
                ? [{ value: filters.project, label: filters.project }]
                : []),
              ...(projects.data?.items.map((p) => ({
                value: p.name,
                label: p.display_name || p.name,
              })) || []),
            ]}
          />
        )}
        <SelectField
          label="Time window"
          value={String(filters.since_seconds || 3600)}
          onValueChange={(value) => change({ ...filters, since_seconds: Number(value) })}
          options={[
            ...(filters.since_seconds && ![900, 3600, 21600, 86400].includes(filters.since_seconds)
              ? [
                  {
                    value: String(filters.since_seconds),
                    label: `Last ${filters.since_seconds} seconds`,
                  },
                ]
              : []),
            { value: '900', label: 'Last 15 minutes' },
            { value: '3600', label: 'Last hour' },
            { value: '21600', label: 'Last 6 hours' },
            { value: '86400', label: 'Last 24 hours' },
          ]}
        />
        <SelectField
          label="Response"
          value={String(filters.status || '')}
          onValueChange={(value) =>
            change({ ...filters, status: value ? Number(value) : undefined })
          }
          options={[
            { value: '', label: 'All responses' },
            ...(filters.status && ![2, 3, 4, 5].includes(filters.status)
              ? [{ value: String(filters.status), label: `HTTP ${filters.status}` }]
              : []),
            ...[2, 3, 4, 5].map((n) => ({
              value: String(n),
              label: `${n}xx ${['Success', 'Redirect', 'Client error', 'Server error'][n - 2]}`,
            })),
          ]}
        />
        <SelectField
          label="Method"
          value={filters.method || ''}
          onValueChange={(method) => change({ ...filters, method: method || undefined })}
          options={[
            '',
            'GET',
            'POST',
            'PUT',
            'PATCH',
            'DELETE',
            'HEAD',
            'OPTIONS',
            ...(filters.method &&
            !['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'].includes(filters.method)
              ? [filters.method]
              : []),
          ].map((method) => ({ value: method, label: method || 'All methods' }))}
        />
        <label className="flex min-w-0 flex-col gap-1 text-sm">
          Host or path
          <div className="flex gap-2">
            <Input
              className="min-w-0 w-full"
              value={search}
              maxLength={200}
              placeholder="Search requests"
              onChange={(e) => setSearch(e.target.value)}
            />
            <Button type="submit" size="sm">
              Search
            </Button>
          </div>
        </label>
      </form>
      {!applicationId && (filters.application_id || filters.service || filters.environment) && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span>
            Filtered to {filters.service || 'application'}
            {filters.environment ? ` · ${filters.environment}` : ''}
          </span>
          <Button
            size="sm"
            variant="ghost"
            onClick={() =>
              change({
                ...filters,
                application_id: undefined,
                service: undefined,
                environment: undefined,
              })
            }
          >
            Clear service filter
          </Button>
        </div>
      )}
      {result.data && (
        <div
          className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground"
          role="status"
        >
          <span>
            {result.data.collection.message}{' '}
            {result.data.collection.gap_count > 0 &&
              `${result.data.collection.gap_count.toLocaleString()} collection gaps detected since setup.`}
          </span>
          <span>
            Retained: up to {result.data.retention_hours}h /{' '}
            {result.data.max_entries.toLocaleString()} requests across this installation.
          </span>
        </div>
      )}
      {result.isPending ? (
        <Loading />
      ) : result.isError ? (
        <ErrorState error={result.error} />
      ) : entries.length === 0 ? (
        <Empty
          icon="activity"
          title="No captured requests"
          description="Send a request to a public service URL, or widen your filters. Collection starts when this feature is installed; earlier traffic is not backfilled."
        />
      ) : (
        <>
          <div className="flex flex-wrap justify-between gap-2 text-xs text-muted-foreground">
            <span>
              {entries.length} on this page · {entries.filter((e) => e.status >= 400).length} errors
              ·{' '}
              {page.cursor
                ? 'Historical page; automatic updates paused'
                : live
                  ? 'Updates every 5 seconds'
                  : 'Updates paused'}
            </span>
            <span>Times shown in your local timezone</span>
          </div>
          <div className="overflow-x-auto rounded-md border border-border">
            <table className="w-full min-w-[720px] text-left text-sm">
              <thead className="bg-[var(--surface-2)] text-xs text-muted-foreground">
                <tr>
                  {['Time', 'Request', 'Service', 'Response', 'Duration', ''].map((label, i) => (
                    <th className="px-3 py-2 font-medium" key={i}>
                      {label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {entries.map((entry) => (
                  <tr key={entry.id} className="border-t border-border hover:bg-[var(--surface-2)]">
                    <td className="whitespace-nowrap px-3 py-3 text-xs">
                      {timestamp(entry.timestamp)}
                    </td>
                    <td className="max-w-[320px] px-3 py-3">
                      <span className="mr-2 font-mono text-xs">{entry.method}</span>
                      <span className="break-all font-mono text-xs">{entry.path || '/'}</span>
                      <div
                        className="mt-1 truncate text-xs text-muted-foreground"
                        title={entry.host}
                      >
                        {entry.host || 'Unmatched host'}
                      </div>
                    </td>
                    <td className="max-w-[180px] break-words px-3 py-3">
                      {entry.service || 'Unmatched route'}
                      <div className="mt-1 text-xs text-muted-foreground">
                        {entry.application
                          ? `${entry.application} · ${entry.project}`
                          : 'Ingress only'}
                      </div>
                    </td>
                    <td className="px-3 py-3">
                      <HTTPStatus value={entry.status} />
                    </td>
                    <td className="whitespace-nowrap px-3 py-3 text-xs">{ms(entry.duration_ms)}</td>
                    <td className="px-3 py-3">
                      <Button
                        size="sm"
                        variant="ghost"
                        aria-label={`Inspect ${entry.method} ${entry.path || '/'} request`}
                        onClick={() => setSelected(entry)}
                      >
                        Inspect
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="flex justify-end gap-2">
            <Button
              size="sm"
              variant="secondary"
              disabled={!page.previous.length}
              onClick={() =>
                setPage({
                  cursor: page.previous.at(-1) || '',
                  previous: page.previous.slice(0, -1),
                })
              }
            >
              Newer
            </Button>
            <Button
              size="sm"
              variant="secondary"
              disabled={!result.data?.next_cursor}
              onClick={() =>
                setPage({
                  cursor: result.data!.next_cursor,
                  previous: [...page.previous, page.cursor],
                })
              }
            >
              Older
            </Button>
          </div>
        </>
      )}
      <Dialog
        open={!!selected}
        onOpenChange={(open) => {
          if (!open) setSelected(null)
        }}
        title="Request details"
        description="Observed ingress data. Timings are milliseconds; unavailable measurements are marked explicitly."
        wide
      >
        {selected && (
          <div className="flex max-h-[65vh] min-w-0 flex-col gap-4 overflow-y-auto p-4">
            <div className="flex flex-wrap items-center gap-2">
              <strong>{selected.method}</strong>
              <HTTPStatus value={selected.status} />
              <span>{ms(selected.duration_ms)}</span>
              <Copy value={JSON.stringify(selected, null, 2)} />
            </div>
            <div className="break-all rounded-md bg-[var(--surface-2)] p-3 font-mono text-xs">
              {selected.host}
              {selected.path}
            </div>
            <dl className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-2">
              {Object.entries({
                Time: timestamp(selected.timestamp),
                'Application / service': selected.application
                  ? `${selected.application} / ${selected.service}`
                  : 'Unmatched ingress route',
                'HTTP protocol': selected.protocol,
                TLS: selected.tls || 'No TLS at this ingress',
                'Response bytes': selected.bytes.toLocaleString(),
                'Client network (masked)': selected.client_network || 'Unavailable',
                'Receive request': ms(selected.request_ms),
                'Backend queue': ms(selected.queue_ms),
                'Connect to backend': ms(selected.connect_ms),
                'Backend response headers': ms(selected.response_ms),
                Retries: selected.retries,
                'Active connections at backend': selected.connections,
                Frontend: selected.frontend,
                Backend: selected.backend,
                'Selected server': selected.server,
                'Ingress pod': selected.ingress_pod,
                'Termination flags': selected.termination,
                'Record ID': selected.id,
              }).map(([name, value]) => (
                <div key={name} className="min-w-0">
                  <dt className="text-xs text-muted-foreground">{name}</dt>
                  <dd className="mt-1 break-all font-mono text-xs">{value}</dd>
                </div>
              ))}
            </dl>
            <p className="text-xs text-muted-foreground">
              Backend timings can overlap; they are not an application trace. “----” termination
              flags indicate a normal completion. Other flags can explain timeouts or disconnects.
              No raw headers, query strings, cookies or bodies are retained.
            </p>
            {selected.application_id && (
              <Button asChild size="sm">
                <Link
                  to="/applications/$applicationId"
                  params={{ applicationId: selected.application_id }}
                  search={{ service: selected.service, tab: 'requests' }}
                >
                  Open service requests
                </Link>
              </Button>
            )}
          </div>
        )}
      </Dialog>
    </div>
  )
}

function RequestRouting({
  applicationId,
  service,
  onHost,
}: {
  applicationId: string
  service: string
  onHost: (host: string) => void
}) {
  const result = useQuery({
    queryKey: ['request-routing', applicationId, service],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/services/{service}/requests/routing', {
          signal,
          params: { path: { id: applicationId, service } },
        }),
      ),
    refetchInterval: 15000,
    refetchIntervalInBackground: false,
    gcTime: 0,
    retry: false,
  })
  const data = result.data
  return (
    <section className="flex min-w-0 flex-col gap-3">
      <div className="flex items-center gap-2">
        <h2 className="text-sm font-semibold">Request routing</h2>
        <HeadingHelp title="Request routing">
          Current Kubernetes ingress rules and ready endpoints. This is the observed route
          configuration, not proof that a specific request visited every endpoint. Select a host to
          filter its requests.
        </HeadingHelp>
      </div>
      {result.isPending ? (
        <Loading />
      ) : result.isError ? (
        <ErrorState error={result.error} />
      ) : (
        data && (
          <>
            <div className="grid min-w-0 grid-cols-1 items-stretch gap-3 lg:grid-cols-[1fr_auto_1fr_auto_1fr]">
              <div className="min-w-0 rounded-md border border-border bg-card p-3">
                <h3 className="mb-2 text-xs font-semibold text-muted-foreground">Public routes</h3>
                <div className="flex max-h-44 flex-col gap-2 overflow-y-auto">
                  {data.routes.length ? (
                    data.routes.map((route, i) => (
                      <button
                        key={i}
                        className="interactive min-w-0 break-all text-left text-xs hover:text-primary"
                        onClick={() => onHost(route.host)}
                      >
                        {route.tls ? 'https' : 'http'}://{route.host}
                        {route.path}
                        <span className="block text-muted-foreground">
                          Ingress: {route.ingress} · port {route.port}
                        </span>
                      </button>
                    ))
                  ) : (
                    <span className="text-xs">No HTTP ingress route</span>
                  )}
                </div>
              </div>
              <span
                className="pointer-events-none justify-self-center self-center text-center text-muted-foreground rotate-90 lg:rotate-0"
                aria-hidden="true"
              >
                →
              </span>
              <div className="min-w-0 rounded-md border border-border bg-card p-3">
                <h3 className="mb-2 text-xs font-semibold text-muted-foreground">
                  Kubernetes service
                </h3>
                <p className="break-all text-sm">{data.service}</p>
                <p className="mt-2 break-all font-mono text-xs text-muted-foreground">
                  {data.namespace}
                </p>
                <p className="mt-2 text-xs">HAProxy selects an available backend.</p>
              </div>
              <span
                className="pointer-events-none justify-self-center self-center text-center text-muted-foreground rotate-90 lg:rotate-0"
                aria-hidden="true"
              >
                →
              </span>
              <div className="min-w-0 rounded-md border border-border bg-card p-3">
                <h3 className="mb-2 text-xs font-semibold text-muted-foreground">
                  Backend endpoints
                </h3>
                <div className="flex max-h-44 flex-col gap-2 overflow-y-auto">
                  {data.endpoints.length ? (
                    data.endpoints.map((ep, i) => (
                      <div key={i} className="min-w-0 text-xs">
                        <span className="break-all">{ep.pod || ep.address}</span>
                        <div className="mt-1 flex flex-wrap items-center gap-2">
                          <span className="break-all font-mono text-muted-foreground">
                            {ep.address}
                          </span>
                          <Status small value={ep.ready ? 'ready' : 'not ready'} />
                        </div>
                      </div>
                    ))
                  ) : (
                    <span className="text-xs">No observed endpoints</span>
                  )}
                </div>
              </div>
            </div>
            {data.warnings.map((warning) => (
              <p className="text-xs text-muted-foreground" key={warning}>
                {warning}
              </p>
            ))}
            <span className="text-xs text-muted-foreground">
              Observed {timestamp(data.observed_at)} · internal service-to-service and raw TCP
              traffic is not logged here.
            </span>
          </>
        )
      )}
    </section>
  )
}
