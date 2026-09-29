import { useEffect, useMemo, useRef, useState } from 'react'
import { WorkflowLogLines } from './workflow-log-lines'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
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
  Download,
  SlidersHorizontal,
  LoaderCircle,
  XCircle,
} from 'lucide-react'
import { client, unwrap } from '../lib/client'
import type { Application } from '../lib/types'
import type { components } from '../lib/api.generated'
import { workflowJobPresentation } from '../lib/actions-workflow'
import {
  cleanWorkflowLog,
  jobDuration,
  workflowStepOutput,
  groupWorkflowLines,
  workflowLogPage,
  workflowLogDownload,
} from '../lib/actions-logs'
import { canAccess, useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Empty, ErrorState, Loading, Note } from './shared'
import { ManagedActionsJobCancel } from './managed-actions-cancel'
import { ManagedActionsHold } from './managed-actions-hold'

type Job = components['schemas']['ActionsJob']
type Line = { number: number; text: string; rawText?: string }
type PresentedJob = Job & { display: ReturnType<typeof workflowJobPresentation> }
const runKey = (job: PresentedJob) => job.display.runKey
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
  const all = useMemo(
    () =>
      (jobs.data?.items || []).map((item) => ({ ...item, display: workflowJobPresentation(item) })),
    [jobs.data?.items],
  )
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
      {pool && <ManagedActionsHold application={application} service={service} />}
      {pool?.pool.removed && (
        <Note>This pool is being removed. Runner registration cleanup is still in progress.</Note>
      )}
      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(180px,280px)_auto] items-end">
        <SelectField
          label="Run"
          disabled={jobs.isPending || Boolean(jobs.error && !jobs.data)}
          value={run}
          onValueChange={(value) => select({ run: value, job: undefined })}
          options={
            runs.length
              ? runs.map((item) => ({
                  value: runKey(item),
                  label: item.display.option,
                }))
              : [
                  {
                    value: '',
                    label: jobs.isPending
                      ? 'Loading runs…'
                      : jobs.error
                        ? 'Runs unavailable'
                        : 'No runs yet',
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
          Older job details exceed this history window. Open the provider for the full history.
        </Note>
      )}
      {jobs.error && <ErrorState error={jobs.error} retry={() => void jobs.refetch()} />}
      {jobs.isPending ? (
        <Loading />
      ) : jobs.error && !jobs.data ? null : !all.length ? (
        <Empty
          title="No jobs recorded yet"
          description="Jobs appear after a managed runner receives a workflow or pipeline. Provider queues may include jobs not yet assigned to this pool."
        />
      ) : !header || !selected ? (
        <Empty
          title="This selection is unavailable"
          description="The job may have aged out of the recent history or belong to another runner. Choose a run or reset the filters."
          action={
            <Button onClick={() => select({ runner: undefined, run: undefined, job: undefined })}>
              Show recent runs
            </Button>
          }
        />
      ) : (
        <>
          <WorkflowRunHeader item={header} />
          <div className="grid grid-cols-1 min-w-0 gap-4 md:grid-cols-[250px_minmax(0,1fr)]">
            <nav className="hidden md:block min-w-0" aria-label="Jobs in this run">
              <h3 className="text-xs muted-text uppercase tracking-wide mb-2">
                Jobs · {runJobs.length}
              </h3>
              <ul className="grid gap-1">
                {runJobs.map((item) => {
                  const status = item.display.status
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
                          <span className="block text-sm wrap-anywhere">{item.display.name}</span>
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
                    label: item.display.name,
                  }))}
                />
              </div>
              <WorkflowJob
                key={selected.slot_id}
                item={selected}
                application={application}
                service={service}
                canReadLogs={canAccess(scope.identity, application.project, 'logs:read')}
              />
            </div>
          </div>
        </>
      )}
      <p className="text-xs muted-text">
        Showing up to 100 jobs from the last 30 days. Jobs are shown only for runners in this pool.
        Queue status is available from the provider.
      </p>
    </section>
  )
}

export function WorkflowRunHeader({ item }: { item: Job }) {
  const view = workflowJobPresentation(item)
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border pb-3">
      <div className="min-w-0">
        <h2 className="text-base font-semibold wrap-anywhere">{view.title}</h2>
        <p className="text-sm muted-text wrap-anywhere">{view.context}</p>
      </div>
      {view.href && (
        <Button size="sm" asChild>
          <a href={view.href} target="_blank" rel="noreferrer">
            View on {view.providerName} <ExternalLink size={14} />
          </a>
        </Button>
      )}
    </div>
  )
}

export function WorkflowJob({
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
  const view = workflowJobPresentation(item)
  const details = view.details
  const [expanded, setExpanded] = useState<number | null>(null)
  const [mode, setMode] = useState<'steps' | 'all'>('steps')
  const [followStep, setFollowStep] = useState(true)
  // Keep the display choice across step switches, full output and live refreshes.
  const [wrap, setWrap] = useState(true)
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
  const steps = details?.steps || []
  const lines = useMemo(
    () =>
      (logs.data?.lines || []).map((line) => ({
        ...line,
        rawText: line.text,
        text: cleanWorkflowLog(line.text),
      })),
    [logs.data?.lines],
  )
  const output = useMemo(() => workflowStepOutput(lines, steps), [lines, steps])
  const suggested =
    steps.find((step) =>
      ['failure', 'timed_out', 'action_required'].includes(step.conclusion || ''),
    ) ||
    steps.find((step) => step.status === 'in_progress') ||
    [...steps].reverse().find((step) => step.started_at) ||
    steps[0]
  const active = followStep ? suggested?.number : expanded
  const status = view.status
  const allOutput = mode === 'all' || steps.length === 0
  return (
    <>
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="font-semibold wrap-anywhere">{view.name}</h3>
          <details className="text-xs muted-text">
            <summary className="min-h-11 cursor-pointer py-3">Job details</summary>
            <p className="break-all">Runner: hakopod-{item.slot_id}</p>
            {view.jobID && <p className="break-all">Job: {view.jobID}</p>}
          </details>
        </div>
        <span className="flex items-center gap-2 text-sm">
          <StateIcon status={status} />
          {label(status)}
          {details?.started_at && ` · ${jobDuration(details.started_at, details.completed_at)}`}
        </span>
      </div>
      <ManagedActionsJobCancel application={application} service={service} item={item} />
      {view.discoveryMessage && <Note>{view.discoveryMessage}</Note>}
      {details &&
        Date.now() - Date.parse(item.updated_at) > 120000 &&
        details.status !== 'completed' && (
          <Note>Job status is out of date. Refresh to check {view.providerName} again.</Note>
        )}
      {details?.steps_truncated && (
        <Note>
          Some step details exceed this window. Open the job on {view.providerName} to see every
          step.
        </Note>
      )}
      {canReadLogs && logs.error && (
        <ErrorState error={logs.error} retry={() => void logs.refetch()} />
      )}
      {logs.data?.message && logs.data.message !== view.discoveryMessage && (
        <p className="text-xs muted-text" role="status">
          {logs.data.message}
        </p>
      )}
      {logs.data?.truncated && (
        <Note>
          This log window is truncated. Open {view.providerName} for the complete retained log.
        </Note>
      )}
      <div
        className="min-w-0 overflow-hidden rounded-lg border border-[var(--hairline)]"
        aria-label="Job steps and output"
      >
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-[var(--hairline)] px-3 py-2">
          <div className="flex flex-wrap items-center gap-2">
            {steps.length > 0 ? (
              <>
                <Button
                  size="sm"
                  variant="ghost"
                  aria-pressed={!allOutput}
                  className={
                    !allOutput
                      ? 'text-[var(--navigation-active)]! bg-transparent! border-transparent! no-underline'
                      : ''
                  }
                  onClick={() => setMode('steps')}
                >
                  Steps <span>{steps.length}</span>
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  aria-pressed={allOutput}
                  className={
                    allOutput
                      ? 'text-[var(--navigation-active)]! bg-transparent! border-transparent! no-underline'
                      : ''
                  }
                  onClick={() => setMode('all')}
                >
                  All output
                </Button>
                {!allOutput && !followStep && (
                  <Button size="sm" variant="ghost" onClick={() => setFollowStep(true)}>
                    Follow current step
                  </Button>
                )}
              </>
            ) : (
              <span className="text-sm font-medium">Job output</span>
            )}
          </div>
          {logs.data && (
            <WorkflowLogStatus source={logs.data.source} observedAt={logs.data.observed_at} />
          )}
        </div>
        {allOutput ? (
          <LogViewer
            wrap={wrap}
            onWrapChange={setWrap}
            lines={lines}
            title="Job"
            source={logs.data?.source || 'unknown'}
            failed={['failure', 'timed_out', 'action_required'].includes(status)}
            loading={logs.isPending && canReadLogs}
            permitted={canReadLogs}
          />
        ) : (
          <>
            {steps.map((value) => {
              const open = active === value.number
              const state = value.conclusion || value.status
              return (
                <div
                  key={value.number}
                  className="border-b border-[var(--hairline)] last:border-b-0"
                >
                  <button
                    type="button"
                    aria-expanded={open}
                    aria-controls={`workflow-step-${item.slot_id}-${value.number}`}
                    className="flex min-h-11 w-full min-w-0 items-center gap-2 px-3 py-3 text-left text-sm focus-visible:outline-2 focus-visible:-outline-offset-2"
                    onClick={() => {
                      setFollowStep(false)
                      setExpanded(open ? null : value.number)
                    }}
                  >
                    {open ? (
                      <ChevronDown size={14} className="shrink-0" aria-hidden="true" />
                    ) : (
                      <ChevronRight size={14} className="shrink-0" aria-hidden="true" />
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
                        wrap={wrap}
                        onWrapChange={setWrap}
                        lines={output.byStep.get(value.number) || []}
                        title={value.name}
                        source={logs.data?.source || 'unknown'}
                        failed={['failure', 'timed_out', 'action_required'].includes(state)}
                        loading={logs.isPending && canReadLogs}
                        permitted={canReadLogs}
                      />
                    )}
                  </div>
                </div>
              )
            })}
            {output.unmatched.length > 0 && (
              <div className="flex flex-wrap items-center gap-2 border-t border-[var(--hairline)] px-3 py-2 text-xs muted-text">
                <span>
                  {output.unmatched.length}{' '}
                  {output.unmatched.length === 1 ? 'line could' : 'lines could'} not be matched to a
                  step.
                </span>
                <Button size="sm" variant="ghost" onClick={() => setMode('all')}>
                  View all output
                </Button>
              </div>
            )}
          </>
        )}
      </div>
      {canReadLogs && logs.data && !['live', 'complete'].includes(logs.data.state) && (
        <Button size="sm" disabled={logs.isFetching} onClick={() => void logs.refetch()}>
          Retry logs
        </Button>
      )}
    </>
  )
}

export function WorkflowLogStatus({ source, observedAt }: { source: string; observedAt: string }) {
  const sourceLabel =
    source === 'runner'
      ? 'Live runner output'
      : source === 'github'
        ? 'GitHub logs'
        : source === 'gitlab'
          ? 'GitLab logs'
          : ''
  return (
    <span className="text-xs muted-text">
      {sourceLabel && `${sourceLabel} · `}
      Checked {new Date(observedAt).toLocaleTimeString()}
    </span>
  )
}

function LogViewer({
  wrap,
  onWrapChange,
  lines,
  title,
  loading,
  permitted,
  failed,
  source,
}: {
  wrap: boolean
  onWrapChange: (wrap: boolean) => void
  lines: Line[]
  title: string
  loading: boolean
  permitted: boolean
  failed: boolean
  source: string
}) {
  const [search, setSearch] = useState('')
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
    <section className="min-w-0" aria-label={`${title} output`}>
      <div className="flex min-w-0 flex-wrap items-center gap-2 border-y border-[var(--hairline)] bg-[var(--surface-2)] px-3 py-2">
        <Input
          className="min-w-0 flex-1 basis-36"
          type="search"
          value={search}
          onChange={(event) => {
            setSearch(event.target.value)
            setWindowEnd(null)
          }}
          aria-label={`Search ${title} log window`}
          placeholder="Find in output"
        />
        <span className="text-xs muted-text" role="status">
          {total} {search ? (total === 1 ? 'match' : 'matches') : total === 1 ? 'line' : 'lines'}
        </span>
        <Menu
          trigger={
            <Button size="sm" variant="ghost">
              <SlidersHorizontal size={14} aria-hidden="true" />
              Display
            </Button>
          }
        >
          <MenuItem onSelect={() => onWrapChange(!wrap)}>
            {wrap ? 'Disable wrapping' : 'Wrap long lines'}
          </MenuItem>
          <MenuItem onSelect={() => setTimestamps(!timestamps)}>
            {timestamps ? 'Hide timestamps' : 'Show timestamps'}
          </MenuItem>
          <MenuItem
            onSelect={() => {
              setWindowEnd(null)
              setFollow(!follow)
            }}
          >
            {follow ? 'Stop following output' : 'Follow new output'}
          </MenuItem>
        </Menu>
        <Button
          size="sm"
          variant="ghost"
          disabled={!lines.length}
          onClick={download}
          aria-label={`Download ${title} log window`}
        >
          <Download size={15} aria-hidden="true" />
        </Button>
      </div>
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
      {(start > 0 || end < total) && (
        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-[var(--hairline)] px-3 py-2 text-xs muted-text">
          <span>
            {start + 1}–{end} of {total}
          </span>
          <div className="flex items-center gap-1">
            <Button
              size="sm"
              variant="ghost"
              disabled={start === 0}
              onClick={() => {
                setWindowEnd(start)
                setFollow(false)
              }}
            >
              Earlier
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={end === total}
              onClick={() => {
                setWindowEnd(Math.min(total, end + 1))
                setFollow(false)
              }}
            >
              Later
            </Button>
            {end < total && (
              <Button size="sm" variant="ghost" onClick={() => setWindowEnd(null)}>
                Latest
              </Button>
            )}
          </div>
        </div>
      )}
    </section>
  )
}
