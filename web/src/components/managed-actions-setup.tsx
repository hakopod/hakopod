import type { ReactNode } from 'react'
import { Card } from '@hakopod/hatch-ui/components/card'
import { Badge } from '@hakopod/hatch-ui/components/badge'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { HeadingHelp } from './shared'
import type { RunnerResources } from '../lib/runner-resources'

export const runnerSteps = ['GitHub', 'Compute', 'Jobs', 'Review']

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
          <span className="font-mono text-xs" aria-hidden="true">
            {index + 1}
          </span>
          {label}
        </button>
      ))}
    </nav>
  )
}

export function RunnerFact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Card className="min-w-0 p-3">
      <div className="text-xs muted-text">{label}</div>
      <div className="mt-1 text-sm font-medium wrap-anywhere">{children}</div>
    </Card>
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
}: {
  effective: RunnerResources
  expanded: boolean
  disabled: boolean
  onChange: (key: keyof RunnerResources, value: string) => void
}) {
  return (
    <details
      open={expanded || undefined}
      className="rounded border border-[var(--hairline)]"
      onInvalidCapture={(event) => {
        event.currentTarget.open = true
      }}
    >
      <summary className="min-h-11 cursor-pointer px-3 py-3 text-sm font-medium">
        Adjust reservations and limits
      </summary>
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
              'At least 200m per runner.',
              '^(?:[0-9]+(?:\\.[0-9]{1,3})?|[0-9]+m)$',
            ],
            [
              'cpu_limit',
              'CPU limit',
              'At least the CPU reservation.',
              '^(?:[0-9]+(?:\\.[0-9]{1,3})?|[0-9]+m)$',
            ],
            [
              'memory_request',
              'Reserved memory',
              'At least 768Mi per runner.',
              '^[0-9]+(?:Ki|Mi|Gi|Ti|k|M|G|T)?$',
            ],
            [
              'memory_limit',
              'Memory limit',
              'At least 4Gi and the memory reservation.',
              '^[0-9]+(?:Ki|Mi|Gi|Ti|k|M|G|T)?$',
            ],
          ] as const
        ).map(([key, label, help, pattern]) => (
          <label key={key} className="grid gap-2 text-sm">
            {label}
            <Input
              required
              value={effective[key] ?? ''}
              disabled={disabled}
              maxLength={32}
              pattern={pattern}
              aria-describedby={`runner-resources-help runner-${key}-help`}
              onChange={(event) => onChange(key, event.target.value)}
            />
            <span id={`runner-${key}-help`} className="field-help">
              {help}
            </span>
          </label>
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
    <div className="grid gap-2">
      <div className="flex items-center gap-2 text-sm">
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
  )
}

export function RunnerWorkflowGuide({ labels }: { labels: string[] }) {
  return (
    <div className="grid gap-3">
      <RunnerLabelChips labels={labels} />
      <pre
        className="min-w-0 overflow-x-auto rounded border border-[var(--hairline)] p-3 text-xs"
        aria-label="Workflow runner selection"
      >
        <code>{`runs-on: ${JSON.stringify(labels)}`}</code>
      </pre>
      <Card className="grid gap-2 p-3">
        <div className="flex items-center gap-2 text-sm font-medium">
          Reuse work between jobs{' '}
          <HeadingHelp title="Caching">
            Use actions/cache or your language setup action in the workflow. GitHub controls
            repository and branch access. Include runner architecture and a dependency lockfile hash
            in cache keys. Each runner still starts with a clean workspace.
          </HeadingHelp>
        </div>
        <p className="text-sm muted-text">
          Restore dependency caches with GitHub Actions. Use a registry cache for Docker build
          layers.
        </p>
        <span className="text-xs muted-text">
          Configure caching in your workflow; creating a pool does not enable it automatically.
        </span>
      </Card>
    </div>
  )
}
