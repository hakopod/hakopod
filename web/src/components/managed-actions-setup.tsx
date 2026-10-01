import { useId, useRef, useState, type ReactNode } from 'react'
import { Check, ChevronRight, Minus, Plus, X } from 'lucide-react'
import { Badge } from '@hakopod/hatch-ui/components/badge'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { HeadingHelp } from './shared'
import type { RunnerResources } from '../lib/runner-resources'

export const runnerSteps = ['Connect', 'Compute', 'Workflow', 'Review']

export function RunnerSteps({
  step,
  visited,
  busy,
  onStep,
}: {
  step: number
  visited: number
  busy: boolean
  onStep: (step: number) => void
}) {
  return (
    <nav
      aria-label="Runner pool setup steps"
      className="hako-page-steps grid grid-cols-4 gap-1 border-b border-[var(--hairline)] pb-3 sm:flex sm:gap-6"
    >
      {runnerSteps.map((label, index) => (
        <button
          key={label}
          type="button"
          aria-current={step === index ? 'step' : undefined}
          disabled={busy || index > visited || (index === 3 && step !== 3)}
          onClick={() => onStep(index)}
          className={`flex min-h-11 min-w-0 items-center justify-center gap-2 rounded-sm px-1 text-sm focus-visible:outline-2 focus-visible:outline-offset-2 bg-transparent! disabled:bg-transparent! disabled:opacity-45! ${step === index ? 'text-[var(--navigation-active)]!' : 'muted-text'}`}
        >
          <span className="hidden font-mono text-xs sm:inline" aria-hidden="true">
            {index + 1}
          </span>
          {label}
        </button>
      ))}
    </nav>
  )
}

type Choice = {
  value: string
  label: string
  detail?: string
  facts?: { label: string; value: string; detail?: string }[]
  disabled?: boolean
}

export function RunnerChoices({
  label,
  value,
  options,
  disabled,
  onChange,
  compact = false,
}: {
  label: string
  value: string
  options: Choice[]
  disabled?: boolean
  onChange: (value: string) => void
  compact?: boolean
}) {
  const name = useId()
  return (
    <fieldset className="grid min-w-0 gap-2" disabled={disabled}>
      <legend className="mb-2 text-sm font-medium">{label}</legend>
      <div
        className={compact ? 'flex flex-wrap gap-2' : 'grid gap-2 sm:grid-cols-2 xl:grid-cols-3'}
      >
        {options.map((option) => (
          <label
            key={option.value}
            className={`relative min-w-0 ${compact ? 'self-start' : ''} ${option.disabled || disabled ? 'cursor-not-allowed opacity-50' : 'cursor-pointer'}`}
          >
            <input
              className="peer sr-only"
              type="radio"
              name={name}
              value={option.value}
              checked={value === option.value}
              disabled={option.disabled}
              onChange={() => onChange(option.value)}
            />
            <span
              className={`flex min-h-11 items-start gap-2 border border-[var(--hairline)] text-sm transition-colors peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-[var(--accent)] peer-checked:border-[var(--accent)] peer-checked:bg-[color-mix(in_srgb,var(--accent)_6%,transparent)] ${compact ? 'h-auto items-center rounded-full px-4 py-2' : 'h-full rounded-md px-3 py-2.5'}`}
            >
              <span className="min-w-0 flex-1">
                <span className="block font-medium wrap-anywhere">{option.label}</span>
                {option.detail && (
                  <span className="mt-1 block text-xs leading-5 muted-text wrap-anywhere">
                    {option.detail}
                  </span>
                )}
                {option.facts && (
                  <span className="mt-2 grid grid-cols-2 gap-2 border-t border-[var(--hairline)] pt-2">
                    {option.facts.map((fact) => (
                      <span key={fact.label} className="grid gap-0.5">
                        <span className="text-xs muted-text">{fact.label}</span>
                        <strong className="text-lg font-medium tabular-nums">{fact.value}</strong>
                        {fact.detail && <span className="text-xs muted-text">{fact.detail}</span>}
                      </span>
                    ))}
                  </span>
                )}
              </span>
              {(!compact || value === option.value) && (
                <span
                  className={`mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border ${value === option.value ? 'border-[var(--accent)] text-[var(--accent)]' : 'border-[var(--hairline)]'}`}
                  aria-hidden="true"
                >
                  {value === option.value && <Check size={11} />}
                </span>
              )}
            </span>
          </label>
        ))}
      </div>
    </fieldset>
  )
}

export function RunnerCapacity({
  value,
  maximum,
  reservationLabel,
  disabled,
  onChange,
}: {
  value: string
  maximum: number
  reservationLabel: string
  disabled: boolean
  onChange: (value: string) => void
}) {
  return (
    <div className="grid min-w-0 content-start gap-3 lg:row-span-3 lg:grid-rows-subgrid">
      <div className="flex min-h-8 items-center gap-2 text-sm font-medium">
        Concurrent jobs
        <HeadingHelp title="Concurrent jobs">One isolated runner for each job slot.</HeadingHelp>
      </div>
      <div className="grid min-h-12 grid-cols-[3rem_5rem_3rem_minmax(0,1fr)] content-start items-start gap-2 [&>.field-error]:col-span-full [&>.field-error]:row-start-2">
        <Button
          type="button"
          size="icon"
          className="col-start-1 row-start-1 h-12! w-12!"
          variant="ghost"
          disabled={disabled || Number(value) <= 1}
          aria-label="Fewer concurrent jobs"
          onClick={() => onChange(String(Number(value) - 1))}
        >
          <Minus size={16} />
        </Button>
        <Input
          className="h-12! w-20 text-center"
          type="number"
          aria-label="Concurrent jobs"
          min={1}
          max={maximum}
          required
          value={value}
          disabled={disabled}
          onChange={(event) => onChange(event.target.value)}
        />
        <Button
          type="button"
          size="icon"
          className="col-start-3 row-start-1 h-12! w-12!"
          variant="ghost"
          disabled={disabled || Number(value) >= maximum}
          aria-label="More concurrent jobs"
          onClick={() => onChange(String(Number(value) + 1))}
        >
          <Plus size={16} />
        </Button>
      </div>
      <p className="text-sm muted-text" role="status">
        {reservationLabel}
      </p>
    </div>
  )
}

export function RunnerField({
  label,
  help,
  helpId,
  children,
}: {
  label: string
  help?: ReactNode
  helpId?: string
  children: ReactNode
}) {
  return (
    <label className="grid min-w-0 content-start gap-2 text-sm sm:row-span-3 sm:grid-rows-subgrid">
      <span>{label}</span>
      <span className="grid min-w-0 content-start gap-2">{children}</span>
      <span id={helpId} className="field-help">
        {help}
      </span>
    </label>
  )
}

export function RunnerLabels({
  value,
  onChange,
  disabled,
  label = 'Workflow labels',
  provider = 'github',
}: {
  value: string
  onChange: (value: string) => void
  disabled: boolean
  label?: string
  provider?: string
}) {
  const [draft, setDraft] = useState('')
  const [error, setError] = useState('')
  const input = useRef<HTMLInputElement>(null)
  const errorId = useId()
  const labels = [
    ...new Set(
      value
        .split(',')
        .map((part) => part.trim())
        .filter(Boolean),
    ),
  ]
  function add() {
    const added = draft
      .split(',')
      .map((part) => part.trim())
      .filter(Boolean)
    const next = [...new Set([...labels, ...added])]
    const validation = runnerLabelError(next, provider)
    if (validation) {
      setError(validation)
      input.current?.setCustomValidity(validation)
      return
    }
    onChange(next.join(', '))
    setDraft('')
    setError('')
    input.current?.setCustomValidity('')
  }
  return (
    <div className="grid gap-2">
      <span className="text-sm font-medium">{label}</span>
      <div className="flex min-w-0 flex-wrap items-center gap-2 rounded-lg border border-[var(--hairline)] p-2">
        {labels.map((item) => (
          <span
            key={item}
            className="inline-flex max-w-full items-center gap-1 rounded-full bg-[var(--surface-2)] pl-3 text-sm"
          >
            <span className="break-all">{item}</span>
            <button
              type="button"
              disabled={disabled}
              aria-label={`Remove ${item}`}
              className="flex size-11 shrink-0 items-center justify-center rounded-full focus-visible:outline-2"
              onClick={() => {
                onChange(labels.filter((existing) => existing !== item).join(', '))
                setError('')
                input.current?.setCustomValidity('')
              }}
            >
              <X size={13} aria-hidden="true" />
            </button>
          </span>
        ))}
        <Input
          ref={input}
          aria-invalid={!!error}
          aria-describedby={error ? errorId : undefined}
          aria-label={`Add ${label.toLowerCase()}`}
          className="min-w-28 flex-1 basis-28 border-0"
          value={draft}
          maxLength={520}
          disabled={disabled}
          placeholder="Add a label"
          onChange={(event) => {
            setDraft(event.target.value)
            setError('')
            event.target.setCustomValidity('')
          }}
          onKeyDown={(event) => {
            if (event.key === 'Enter' || event.key === ',') {
              event.preventDefault()
              add()
            }
          }}
          onBlur={add}
        />
      </div>
      {error && (
        <p id={errorId} role="alert" className="text-sm error">
          {error}
        </p>
      )}
      <span className="text-xs muted-text">
        {provider === 'bitbucket'
          ? 'Up to nine custom labels: lowercase letters, numbers and dots. Platform labels follow your architecture.'
          : 'Press Enter or comma to add. Use the same labels in your workflow.'}
      </span>
    </div>
  )
}

export function runnerLabelError(labels: string[], provider: string) {
  if (provider === 'bitbucket') {
    if (labels.length > 9) return 'Use up to nine custom labels.'
    if (
      labels.some(
        (label) =>
          !/^[a-z0-9.]{1,64}$/.test(label) ||
          label.startsWith('hakopod.owner.') ||
          ['self.hosted', 'linux', 'linux.arm64', 'windows', 'macos', 'linux.shell'].includes(
            label,
          ),
      )
    )
      return 'Use lowercase letters, numbers and dots; at most 64 characters. Platform and ownership labels are set by Hakopod.'
  } else {
    if (labels.length > 8) return 'Use up to eight labels.'
    if (labels.some((label) => !/^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/.test(label)))
      return 'Use letters, numbers, dots, underscores or hyphens; at most 64 characters.'
    if (new Set(labels.map((label) => label.toLowerCase())).size !== labels.length)
      return 'Use distinct labels; uppercase and lowercase names count as the same label.'
  }
  return ''
}

export function runnerWorkflowLabels(labels: string[], provider: string, architecture: string) {
  if (provider !== 'bitbucket') return labels
  return [
    'self.hosted',
    ...(architecture ? [architecture === 'arm64' ? 'linux.arm64' : 'linux'] : []),
    ...labels.filter((label) => !['self.hosted', 'linux', 'linux.arm64'].includes(label)),
  ]
}

export function runnerCustomLabels(labels: string[] | undefined, provider: string) {
  return (labels ?? ['hakopod'])
    .filter(
      (label) =>
        provider !== 'bitbucket' || !['self.hosted', 'linux', 'linux.arm64'].includes(label),
    )
    .join(', ')
}

export function RunnerFact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0 rounded-lg bg-[var(--surface-2)] p-3">
      <div className="text-xs muted-text">{label}</div>
      <div className="mt-1 text-sm font-medium wrap-anywhere">{children}</div>
    </div>
  )
}

export function RunnerDisclosureSummary({
  children,
  inset = false,
}: {
  children: ReactNode
  inset?: boolean
}) {
  return (
    <summary
      className={`flex min-h-11 cursor-pointer list-none items-center justify-between gap-3 py-3 text-sm font-medium [&::-webkit-details-marker]:hidden ${inset ? 'px-3' : ''}`}
    >
      <ChevronRight
        className="size-4 shrink-0 transition-transform group-open:rotate-90"
        aria-hidden="true"
      />
      <span className="flex-1">{children}</span>
    </summary>
  )
}

type RunnerPoolSummaryProps = {
  provider: string
  target: string
  pool: string
  architecture: string
  replicas: string
  placement: string
  resources: string
  workspace: string
  labels: string[]
  mode: 'mobile' | 'desktop'
}

function RunnerPoolSummaryBody({
  provider,
  target,
  pool,
  architecture,
  replicas,
  placement,
  resources,
  workspace,
  labels,
  mode: _mode,
}: RunnerPoolSummaryProps) {
  return (
    <div className="grid gap-3 text-sm">
      <dl className="grid gap-4">
        <div className="grid min-w-0 gap-1">
          <dt className="text-xs muted-text">Connection</dt>
          <dd className="font-medium">{provider}</dd>
          <dd className="text-xs muted-text wrap-anywhere">{target || 'Target not selected'}</dd>
          <dd className="text-xs muted-text wrap-anywhere">{pool || 'Pool not named'}</dd>
        </div>
        <div className="grid min-w-0 gap-1 border-t border-[var(--hairline)] pt-3">
          <dt className="text-xs muted-text">Compute</dt>
          <dd className="font-medium">
            {replicas || '0'} {replicas === '1' ? 'job slot' : 'job slots'} ·{' '}
            {architecture || 'Automatic'}
          </dd>
          <dd className="text-xs muted-text wrap-anywhere">
            {placement || 'Any eligible runner node'}
          </dd>
          <dd className="text-xs muted-text wrap-anywhere">{resources}</dd>
        </div>
        <div className="grid min-w-0 gap-1 border-t border-[var(--hairline)] pt-3">
          <dt className="text-xs muted-text">Job workspace</dt>
          <dd>{workspace}</dd>
        </div>
      </dl>
      {labels.length > 0 && (
        <div className="grid gap-2 border-t border-[var(--hairline)] pt-3">
          <span className="text-xs muted-text">Workflow labels</span>
          <RunnerLabelChips labels={labels} />
        </div>
      )}
    </div>
  )
}

export function RunnerPoolSummary(props: RunnerPoolSummaryProps) {
  if (props.mode === 'mobile')
    return (
      <details className="group rounded-md border border-[var(--hairline)] lg:hidden">
        <RunnerDisclosureSummary inset>Pool summary</RunnerDisclosureSummary>
        <div className="border-t border-[var(--hairline)] px-3 py-3">
          <RunnerPoolSummaryBody {...props} />
        </div>
      </details>
    )
  return (
    <aside
      className="sticky top-4 hidden min-w-0 rounded-md border border-[var(--hairline)] p-4 lg:grid lg:gap-4"
      aria-label="Pool summary"
    >
      <h2 className="text-sm font-semibold">Pool summary</h2>
      <RunnerPoolSummaryBody {...props} />
    </aside>
  )
}

export function RunnerLabelChips({ labels }: { labels: string[] }) {
  return (
    <div className="flex min-w-0 flex-wrap gap-2" aria-label="Workflow labels">
      {labels.slice(0, 16).map((label) => (
        <Badge key={label}>
          <span className="break-all whitespace-normal">{label}</span>
        </Badge>
      ))}
    </div>
  )
}

export const runnerResourcePresets: Record<string, RunnerResources> = {
  balanced: { cpu_request: '500m', cpu_limit: '2', memory_request: '1Gi', memory_limit: '4Gi' },
  builds: { cpu_request: '1', cpu_limit: '4', memory_request: '2Gi', memory_limit: '8Gi' },
}

export function RunnerResourceFields({
  effective,
  expanded,
  disabled,
  onChange,
  minimum,
}: {
  effective: RunnerResources
  minimum?: RunnerResources
  expanded: boolean
  disabled: boolean
  onChange: (key: keyof RunnerResources, value: string) => void
}) {
  return (
    <details
      open={expanded || undefined}
      className="group rounded border border-[var(--hairline)]"
      onInvalidCapture={(event) => {
        event.currentTarget.open = true
      }}
    >
      <RunnerDisclosureSummary inset>Adjust reservations and limits</RunnerDisclosureSummary>
      <p id="runner-resources-help" className="field-help px-3 pt-1">
        Reservations set aside capacity. Limits cap usage. Both include Docker and sandbox overhead.
        1000m CPU is one core; 1024Mi memory is 1Gi.
      </p>
      <div className="mt-3 grid gap-3 px-3 pb-3 sm:grid-cols-2">
        {(
          [
            [
              'cpu_request',
              'Reserved CPU',
              `At least ${minimum?.cpu_request || '200m'} per runner.`,
              '^(?:[0-9]+(?:\\.[0-9]{1,3})?|[0-9]+m)$',
            ],
            [
              'cpu_limit',
              'CPU limit',
              minimum?.cpu_limit
                ? `At least ${minimum.cpu_limit}, and no lower than the CPU reservation.`
                : 'No lower than the CPU reservation.',
              '^(?:[0-9]+(?:\\.[0-9]{1,3})?|[0-9]+m)$',
            ],
            [
              'memory_request',
              'Reserved memory',
              `At least ${minimum?.memory_request || '768Mi'} per runner.`,
              '^[0-9]+(?:Ki|Mi|Gi|Ti|k|M|G|T)?$',
            ],
            [
              'memory_limit',
              'Memory limit',
              `At least ${minimum?.memory_limit || '4Gi'} and the memory reservation.`,
              '^[0-9]+(?:Ki|Mi|Gi|Ti|k|M|G|T)?$',
            ],
          ] as const
        ).map(([key, label, help, pattern]) => (
          <RunnerField key={key} label={label} help={help} helpId={`runner-${key}-help`}>
            <Input
              required
              value={effective[key] ?? ''}
              disabled={disabled}
              maxLength={32}
              pattern={pattern}
              aria-describedby={`runner-resources-help runner-${key}-help`}
              onChange={(event) => onChange(key, event.target.value)}
            />
          </RunnerField>
        ))}
      </div>
    </details>
  )
}

export function RunnerPlacement({
  value,
  architecture,
  nodes,
  loading,
  failed,
  refreshing,
  disabled,
  onChange,
  onRetry,
}: {
  value: string
  architecture: string
  nodes: { name: string; architecture: string; available: boolean; reason: string }[] | undefined
  loading: boolean
  failed: boolean
  refreshing: boolean
  disabled: boolean
  onChange: (value: string) => void
  onRetry: () => void
}) {
  const selected = nodes?.find((node) => node.name === value)
  const missing = Boolean(value && !selected)
  const mismatch = Boolean(selected && architecture && selected.architecture !== architecture)
  return (
    <div className="grid min-w-0 content-start gap-3 lg:row-span-3 lg:grid-rows-subgrid">
      <div className="flex min-h-8 items-center gap-2 text-sm font-medium">
        Run on node{' '}
        <HeadingHelp title="Runner placement">
          Use a separate worker node to reserve compute for CI. Only nodes allowed in this
          environment and prepared for Managed Actions can run these jobs.
        </HeadingHelp>
      </div>
      <SelectField
        label="Run on node"
        value={value}
        onValueChange={onChange}
        disabled={disabled || loading || failed}
        options={[
          { value: '', label: 'Automatic · any eligible runner node' },
          ...(missing
            ? [
                {
                  value,
                  label: `${value} · ${nodes ? 'unavailable' : 'saved selection'}`,
                  disabled: true,
                },
              ]
            : []),
          ...(nodes || []).map((node) => ({
            value: node.name,
            label: `${node.name} · ${node.architecture}${!node.available ? ` · ${node.reason}` : architecture && node.architecture !== architecture ? ' · architecture mismatch' : ''}`,
            disabled:
              !node.available || Boolean(architecture && node.architecture !== architecture),
          })),
        ]}
      />
      <div className="grid min-w-0 content-start gap-2">
        {value && (
          <p className="field-help">
            This pool stays on {value}. If it is unavailable, jobs wait; they do not move to another
            node.
          </p>
        )}
        {loading && (
          <p className="field-help" role="status">
            Checking runner nodes…
          </p>
        )}
        {failed && (
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span role="alert">Node choices could not be loaded. Your selection is preserved.</span>
            <Button type="button" size="sm" disabled={refreshing} onClick={onRetry}>
              Retry nodes
            </Button>
          </div>
        )}
        {nodes &&
          !nodes.some(
            (node) => node.available && (!architecture || node.architecture === architecture),
          ) && (
            <p role="status" className="field-help">
              No eligible node is currently available for this architecture. Ask your operator to
              prepare a runner node.
            </p>
          )}
        {nodes && (missing || mismatch || (selected && !selected.available)) && (
          <p role="alert" className="field-help error">
            The selected node cannot currently run this pool. Select another eligible node or
            Automatic.
          </p>
        )}
      </div>
    </div>
  )
}

export function RunnerWorkflowGuide({
  labels,
  provider = 'github',
  architecture = '',
}: {
  labels: string[]
  provider?: string
  architecture?: string
}) {
  const routingLabels = runnerWorkflowLabels(labels, provider, architecture)
  return (
    <div className="grid content-start self-start gap-3">
      <details className="group border-y border-[var(--hairline)]">
        <RunnerDisclosureSummary>Workflow example</RunnerDisclosureSummary>
        <pre
          className="mb-3 min-w-0 overflow-x-auto rounded border border-[var(--hairline)] p-3 text-xs"
          aria-label="Workflow runner selection"
        >
          <code>
            {provider === 'gitlab'
              ? `tags: ${JSON.stringify(labels)}`
              : provider === 'bitbucket'
                ? `runs-on: ${JSON.stringify(routingLabels)}`
                : `runs-on: ${JSON.stringify(labels)}`}
          </code>
        </pre>
      </details>
      <div className="flex items-center gap-2 text-sm">
        Dependency and build caches
        <HeadingHelp title="Caching between jobs">
          {provider === 'github'
            ? 'Use actions/cache or your language setup action. Include architecture and the dependency lockfile hash in each cache key. Docker build layers can use a registry cache.'
            : provider === 'gitlab'
              ? 'Use a registry cache for Docker build layers. GitLab cache persistence depends on the runner cache backend configured by your operator; a fresh runner does not retain its local disk.'
              : 'Configure dependency caches in bitbucket-pipelines.yml and use a registry cache for Docker build layers. This repository-dedicated runner reuses its sandbox; local disk is not a durable cache.'}{' '}
          Creating a pool does not enable caching in the workflow.
        </HeadingHelp>
      </div>
    </div>
  )
}
