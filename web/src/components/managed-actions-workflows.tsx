import { useEffect, useMemo, useRef, useState } from 'react'
import { WorkflowLogLines } from './workflow-log-lines'
import { useQuery } from '@tanstack/react-query'
import { useNavigate, useSearch } from '@tanstack/react-router'
import {
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Circle,
  CircleDashed,
  Clock3,
  ExternalLink,
  LoaderCircle,
  XCircle,
} from 'lucide-react'
import { client, unwrap } from '../lib/client'
import type { Application } from '../lib/types'
import type { components } from '../lib/api.generated'
import {
  cleanWorkflowLog,
  jobDuration,
  logTimestamp,
  groupWorkflowLines,
  workflowLogPage,
  workflowLogDownload,
} from '../lib/actions-logs'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Empty, ErrorState, Loading, Note } from './shared'

type Job = components['schemas']['ActionsJob']
type Line = { number: number; text: string; rawText?: string }
const runKey = (job: Job) =>
  `${job.observation.repository}:${job.observation.run_id}:${job.observation.attempt}`
function StateIcon({ status }: { status: string }) {
  const Icon =
    status === 'success'
      ? CheckCircle2
      : ['failure', 'timed_out', 'action_required'].includes(status)
        ? XCircle
        : status === 'in_progress'
          ? LoaderCircle
          : status === 'queued'
            ? Clock3
            : status === 'skipped' || status === 'cancelled'
              ? CircleDashed
              : Circle
  return (
    <Icon
      aria-hidden="true"
      size={16}
      className={`shrink-0 ${status === 'in_progress' ? 'animate-spin motion-reduce:animate-none' : ''} ${status === 'success' ? 'text-[var(--success)]' : status === 'failure' ? 'text-[var(--destructive)]' : ''}`}
    />
  )
}
const label = (status: string) => status.replaceAll('_', ' ')

export function ManagedActionsWorkflows({
  application,
  service,
}: {
  application: Application
  service: string
}) {
  const navigate = useNavigate()
  const search = useSearch({ from: '/applications/$applicationId' })
  const scope = useScope()
  const jobs = useQuery({
    queryKey: ['actions-jobs', application.id, service],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/actions/{service}/jobs', {
          signal,
          params: { path: { id: application.id, service } },
        }),
      ),
    refetchInterval: 15000,
    refetchIntervalInBackground: false,
    gcTime: 0,
  })
  const pools = useQuery({
    queryKey: ['managed-actions', application.id],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/actions', {
          signal,
          params: { path: { id: application.id } },
        }),
      ),
    refetchInterval: 10000,
    refetchIntervalInBackground: false,
    gcTime: 0,
  })
  const pool = pools.data?.items.find((item) => item.pool.service === service)
  const slots = pool?.slots || []
  const countsKnown = Boolean(pool) && !pools.isError
  const fresh = slots.filter((slot) => Date.now() - Date.parse(slot.updated_at) < 120000)
  const all = jobs.data?.items || []
  const runner = search.runner || ''
  const filtered = runner ? all.filter((job) => job.slot_id === runner) : all
  const runs = [...new Map(filtered.map((job) => [runKey(job), job])).values()]
  const run = search.run || (runs[0] ? runKey(runs[0]) : '')
  const runJobs = filtered.filter((job) => runKey(job) === run)
  const selected = search.job ? runJobs.find((job) => job.slot_id === search.job) : runJobs[0]
  const header = runJobs[0]
  function select(next: { run?: string; job?: string; runner?: string }) {
    void navigate({
      to: '/applications/$applicationId',
      params: { applicationId: application.id },
      search: {
        service,
        tab: 'actions',
        run: search.run,
        job: search.job,
        runner: search.runner,
        ...next,
      },
    })
  }
  const runnerOptions = [
    ...new Set([...slots.map((slot) => slot.id), ...all.map((job) => job.slot_id)]),
  ]
  return (
    <section className="min-w-0 grid grid-cols-1 gap-4" aria-label="Workflow activity">
      <div
        className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm"
        aria-label="Runner pool activity"
      >
        <span>
          <strong>
            {countsKnown ? fresh.filter((slot) => slot.phase === 'busy').length : '—'}
          </strong>{' '}
          Running jobs
        </span>
        <span>
          <strong>
            {countsKnown ? fresh.filter((slot) => slot.phase === 'online').length : '—'}
          </strong>{' '}
          Idle runners
        </span>
        <span>
          <strong>
            {countsKnown
              ? fresh.filter((slot) => !['busy', 'online'].includes(slot.phase)).length
              : '—'}
          </strong>{' '}
          Starting / cleanup
        </span>
        <span className="muted-text">{pool?.pool.config.replicas ?? '—'} configured slots</span>
      </div>
      {pools.error && <ErrorState error={pools.error} retry={() => void pools.refetch()} />}
      {slots.length !== fresh.length && (
        <Note>
          Some runner observations are out of date. Counts include only runners checked within the
          last two minutes.
        </Note>
      )}
      {pool?.pool.message && <Note>{pool.pool.message}</Note>}
      {pool?.pool.removed && (
        <Note>This pool is being removed. Runner registration cleanup is still in progress.</Note>
      )}
      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(180px,280px)_auto] items-end">
        <SelectField
          label="Workflow run"
          disabled={jobs.isPending || Boolean(jobs.error && !jobs.data)}
          value={run}
          onValueChange={(value) => select({ run: value, job: undefined })}
          options={
            runs.length
              ? runs.map((item) => ({
                  value: runKey(item),
                  label: `${item.observation.workflow} #${item.observation.run_number} · Attempt ${item.observation.attempt} · ${item.observation.repository}`,
                }))
              : [
                  {
                    value: '',
                    label: jobs.isPending
                      ? 'Loading workflow runs…'
                      : jobs.error
                        ? 'Workflow runs unavailable'
                        : 'No workflow runs yet',
                  },
                ]
          }
        />
        <SelectField
          label="Runner"
          value={runner}
          onValueChange={(value) =>
            select({ runner: value || undefined, run: undefined, job: undefined })
          }
          options={[
            { value: '', label: 'All runners' },
            ...runnerOptions.map((id) => ({ value: id, label: `hakopod-${id}` })),
          ]}
        />
        <Button
          size="sm"
          disabled={jobs.isFetching || pools.isFetching}
          onClick={() => {
            void jobs.refetch()
            void pools.refetch()
          }}
        >
          Refresh
        </Button>
      </div>
      {jobs.data?.message && <Note>{jobs.data.message}</Note>}
      {jobs.data?.truncated && (
        <Note>
          Older job details exceed this history window. Open GitHub for the full workflow history.
        </Note>
      )}
      {jobs.error && <ErrorState error={jobs.error} retry={() => void jobs.refetch()} />}
      {jobs.isPending ? (
        <Loading />
      ) : jobs.error && !jobs.data ? null : !all.length ? (
        <Empty
          title="No jobs recorded yet"
          description="Jobs appear when a runner using the current Hakopod image starts a workflow. Queued jobs have not been assigned to a runner, so GitHub remains the source for queue status."
        />
      ) : !header || !selected ? (
        <Empty
          title="This selection is unavailable"
          description="The job may have aged out of the recent history or belong to another runner. Choose a workflow run or reset the filters."
          action={
            <Button onClick={() => select({ runner: undefined, run: undefined, job: undefined })}>
              Show recent runs
            </Button>
          }
        />
      ) : (
        <>
          <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border pb-3">
            <div className="min-w-0">
              <h2 className="text-base font-semibold wrap-anywhere">
                {header.observation.workflow}{' '}
                <span className="muted-text">#{header.observation.run_number}</span>
              </h2>
              <p className="text-sm muted-text wrap-anywhere">
                {header.observation.repository} · {header.observation.branch} ·{' '}
                {header.observation.sha.slice(0, 7)} · Attempt {header.observation.attempt}
              </p>
            </div>
            <Button size="sm" asChild>
              <a
                href={`https://github.com/${header.observation.repository}/actions/runs/${header.observation.run_id}/attempts/${header.observation.attempt}`}
                target="_blank"
                rel="noreferrer"
              >
                View on GitHub <ExternalLink size={14} />
              </a>
            </Button>
          </div>
          <div className="grid grid-cols-1 min-w-0 gap-4 md:grid-cols-[250px_minmax(0,1fr)]">
            <nav className="hidden md:block min-w-0" aria-label="Jobs in this workflow run">
              <h3 className="text-xs muted-text uppercase tracking-wide mb-2">
                Jobs · {runJobs.length}
              </h3>
              <ul className="grid gap-1">
                {runJobs.map((item) => {
                  const status = item.job?.conclusion || item.job?.status || 'waiting'
                  return (
                    <li key={item.slot_id}>
                      <button
                        type="button"
                        aria-current={selected.slot_id === item.slot_id ? 'true' : undefined}
                        className={`flex w-full min-w-0 items-start gap-2 text-left px-2 py-3 rounded-sm focus-visible:outline-2 ${selected.slot_id === item.slot_id ? 'text-[var(--navigation-active)]' : 'muted-text'}`}
                        onClick={() => select({ run, job: item.slot_id })}
                      >
                        <StateIcon status={status} />
                        <span className="min-w-0">
                          <span className="block text-sm wrap-anywhere">
                            {item.job?.name || item.observation.job_key}
                          </span>
                          <span className="text-xs">{label(status)}</span>
                        </span>
                      </button>
                    </li>
                  )
                })}
              </ul>
            </nav>
            <div className="min-w-0 grid grid-cols-1 content-start gap-3">
              <div className="md:hidden min-w-0">
                <SelectField
                  label="Job"
                  value={selected.slot_id}
                  onValueChange={(job) => select({ run, job })}
                  options={runJobs.map((item) => ({
                    value: item.slot_id,
                    label: item.job?.name || item.observation.job_key,
                  }))}
                />
              </div>
              <WorkflowJob
                key={selected.slot_id}
                item={selected}
                application={application}
                service={service}
                canReadLogs={scope.can('logs:read')}
              />
            </div>
          </div>
        </>
      )}
      <p className="text-xs muted-text">
        Showing up to 100 jobs from the last 30 days. Jobs are shown only for runners in this pool.
        Queue status is available on GitHub.
      </p>
    </section>
  )
}

function WorkflowJob({
  item,
  application,
  service,
  canReadLogs,
}: {
  item: Job
  application: Application
  service: string
  canReadLogs: boolean
}) {
  const [expanded, setExpanded] = useState<number | 'all'>('all')
  const logs = useQuery({
    queryKey: ['actions-job-logs', application.id, service, item.slot_id],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/actions/{service}/jobs/{slot}/logs', {
          signal,
          params: { path: { id: application.id, service, slot: item.slot_id } },
        }),
      ),
    enabled: canReadLogs,
    refetchInterval: (query) =>
      ['complete', 'permission_denied', 'unavailable'].includes(query.state.data?.state || '')
        ? false
        : 5000,
    refetchIntervalInBackground: false,
    gcTime: 0,
    retry: false,
  })
  const steps = item.job?.steps || []
  const step =
    typeof expanded === 'number' ? steps.find((value) => value.number === expanded) : undefined
  const lines = useMemo(
    () =>
      (logs.data?.lines || [])
        .map((line) => ({ ...line, rawText: line.text, text: cleanWorkflowLog(line.text) }))
        .filter((line) => {
          if (!step) return true
          if (!step.started_at) return false
          const timestamp = logTimestamp(line.text)?.timestamp
          return (
            timestamp !== undefined &&
            timestamp >= Date.parse(step.started_at) &&
            (!step.completed_at || timestamp <= Date.parse(step.completed_at))
          )
        }),
    [logs.data?.lines, step],
  )
  const status = item.job?.conclusion || item.job?.status || 'waiting'
  return (
    <>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="font-semibold wrap-anywhere">
            {item.job?.name || item.observation.job_key}
          </h3>
          <p className="text-xs muted-text break-all">Runner: hakopod-{item.slot_id}</p>
        </div>
        <span className="flex items-center gap-2 text-sm">
          <StateIcon status={status} />
          {label(status)} · {jobDuration(item.job?.started_at, item.job?.completed_at)}
        </span>
      </div>
      {Date.now() - Date.parse(item.updated_at) > 120000 && item.job?.status !== 'completed' && (
        <Note>Job status is out of date. Refresh to check GitHub again.</Note>
      )}
      {item.job?.steps_truncated && (
        <Note>Some step details exceed this window. Open the job on GitHub to see every step.</Note>
      )}
      {steps.length > 0 ? (
        <div className="min-w-0 border border-border rounded-md overflow-hidden">
          {steps.map((value) => {
            const open = expanded === value.number
            const state = value.conclusion || value.status
            return (
              <div key={value.number} className="border-b border-border last:border-b-0">
                <button
                  type="button"
                  aria-expanded={open}
                  aria-controls={`workflow-step-${item.slot_id}-${value.number}`}
                  className="flex w-full min-w-0 items-center gap-2 px-3 py-3 text-left text-sm focus-visible:outline-2 focus-visible:-outline-offset-2"
                  onClick={() => setExpanded(open ? 'all' : value.number)}
                >
                  {open ? (
                    <ChevronDown size={14} className="shrink-0" />
                  ) : (
                    <ChevronRight size={14} className="shrink-0" />
                  )}
                  <StateIcon status={state} />
                  <span className="min-w-0 flex-1 wrap-anywhere">{value.name}</span>
                  <span className="text-xs muted-text shrink-0">
                    {jobDuration(value.started_at, value.completed_at)}
                  </span>
                  <span className="sr-only">{label(state)}</span>
                </button>
                <div id={`workflow-step-${item.slot_id}-${value.number}`} hidden={!open}>
                  {open && (
                    <LogViewer
                      key={value.number}
                      lines={lines}
                      title={value.name}
                      source={logs.data?.source || 'unknown'}
                      failed={['failure', 'timed_out', 'action_required'].includes(
                        step?.conclusion || status,
                      )}
                      loading={logs.isPending && canReadLogs}
                      permitted={canReadLogs}
                    />
                  )}
                </div>
              </div>
            )
          })}
        </div>
      ) : (
        <p className="text-sm muted-text">
          {item.job
            ? 'GitHub has not reported steps yet.'
            : 'Waiting for GitHub to confirm this job and its steps. The credential needs Actions read access.'}
        </p>
      )}
      <div className="flex flex-wrap gap-2 items-center justify-between">
        <Button size="sm" aria-pressed={expanded === 'all'} onClick={() => setExpanded('all')}>
          Full job log
        </Button>
        <span className="text-xs muted-text">
          {logs.data?.source === 'runner'
            ? 'Live runner output'
            : logs.data?.source === 'github'
              ? 'GitHub logs'
              : ''}
          {logs.data && ` · Checked ${new Date(logs.data.observed_at).toLocaleTimeString()}`}
        </span>
      </div>
      {canReadLogs && logs.error && (
        <ErrorState error={logs.error} retry={() => void logs.refetch()} />
      )}
      {logs.data?.message && <Note>{logs.data.message}</Note>}
      {logs.data?.truncated && (
        <Note>This log window is truncated. Open GitHub for the complete retained log.</Note>
      )}
      {expanded === 'all' && (
        <LogViewer
          lines={lines}
          title="Full job log"
          source={logs.data?.source || 'unknown'}
          failed={['failure', 'timed_out', 'action_required'].includes(step?.conclusion || status)}
          loading={logs.isPending && canReadLogs}
          permitted={canReadLogs}
        />
      )}
      {canReadLogs && logs.data && !['live', 'complete'].includes(logs.data.state) && (
        <Button size="sm" disabled={logs.isFetching} onClick={() => void logs.refetch()}>
          Retry logs
        </Button>
      )}
    </>
  )
}

function LogViewer({
  lines,
  title,
  loading,
  permitted,
  failed,
  source,
}: {
  lines: Line[]
  title: string
  loading: boolean
  permitted: boolean
  failed: boolean
  source: string
}) {
  const [search, setSearch] = useState('')
  const [wrap, setWrap] = useState(false)
  const [timestamps, setTimestamps] = useState(false)
  const [follow, setFollow] = useState(false)
  const [windowEnd, setWindowEnd] = useState<number | null>(null)
  const scroll = useRef<HTMLDivElement>(null)
  const parsed = useMemo(() => groupWorkflowLines(lines), [lines])
  const {
    rows: visible,
    start,
    end,
    total,
  } = useMemo(() => workflowLogPage(parsed, search, windowEnd), [parsed, search, windowEnd])
  useEffect(() => {
    if (follow && scroll.current) scroll.current.scrollTop = scroll.current.scrollHeight
  }, [lines, follow])
  function download() {
    const url = URL.createObjectURL(
      new Blob([workflowLogDownload(lines)], { type: 'text/plain;charset=utf-8' }),
    )
    const link = document.createElement('a')
    link.href = url
    link.download = 'workflow-log-window.txt'
    link.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }
  if (!permitted)
    return (
      <p className="p-3 text-sm muted-text">
        Logs require logs:read permission for this application.
      </p>
    )
  return (
    <section className="min-w-0 border border-border rounded-md" aria-label={`${title} output`}>
      <div className="flex flex-wrap items-center gap-2 border-b border-border p-2">
        <Input
          className="min-w-0 flex-1 basis-44"
          type="search"
          value={search}
          onChange={(event) => {
            setSearch(event.target.value)
            setWindowEnd(null)
          }}
          aria-label={`Search ${title} log window`}
          placeholder="Search this log window"
        />
        <span className="text-xs muted-text" role="status">
          {total} {search ? 'matching rows' : 'log rows'}
        </span>
        <Button size="sm" aria-pressed={wrap} onClick={() => setWrap(!wrap)}>
          Wrap
        </Button>
        <Button size="sm" aria-pressed={timestamps} onClick={() => setTimestamps(!timestamps)}>
          Time
        </Button>
        <Button
          size="sm"
          aria-pressed={follow}
          onClick={() => {
            setWindowEnd(null)
            setFollow(!follow)
          }}
        >
          Follow
        </Button>
        <Button size="sm" disabled={!lines.length} onClick={download}>
          Download window
        </Button>
      </div>
      {(start > 0 || end < total) && (
        <div className="flex flex-wrap items-center justify-between gap-2 p-2 text-xs muted-text">
          <span>
            Showing rows {start + 1}–{end} of {total}. Search covers the entire loaded window.
          </span>
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              disabled={start === 0}
              onClick={() => {
                setWindowEnd(start)
                setFollow(false)
              }}
            >
              Earlier lines
            </Button>
            <Button
              size="sm"
              disabled={end === total}
              onClick={() => {
                setWindowEnd(Math.min(total, end + 1))
                setFollow(false)
              }}
            >
              Later lines
            </Button>
            <Button size="sm" disabled={end === total} onClick={() => setWindowEnd(null)}>
              Latest lines
            </Button>
          </div>
        </div>
      )}
      <div
        ref={scroll}
        className="max-h-[520px] min-h-28 min-w-0 overflow-auto font-mono text-xs leading-6"
        tabIndex={0}
        aria-label={`${title} scrollable log`}
        onScroll={(event) => {
          const el = event.currentTarget
          if (el.scrollHeight - el.clientHeight - el.scrollTop > 40) setFollow(false)
        }}
      >
        {loading ? (
          <p className="px-3 muted-text">Loading log output…</p>
        ) : !visible.length ? (
          <p className="px-3 muted-text">
            {search
              ? 'No matching lines in this window.'
              : 'No output available in this window yet.'}
          </p>
        ) : null}
        <WorkflowLogLines
          parsed={parsed}
          rows={visible}
          failed={failed}
          source={source}
          wrap={wrap}
          timestamps={timestamps}
          searching={Boolean(search)}
        />
      </div>
    </section>
  )
}
