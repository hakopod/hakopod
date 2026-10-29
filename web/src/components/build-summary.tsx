import { Status as HatchStatus, type StatusTone } from '@hakopod/hatch-ui/components/status'
import type { components } from '../lib/api.generated'
import { frameworkLabel } from './framework-build-fields'

// Labels and states shared by the source build list, detail and form. Styles live in builds.css.

type Build = components['schemas']['BuildConfig']
type Run = components['schemas']['BuildRun']

export const providerName = (provider: Build['provider']) =>
  provider === 'gitlab' ? 'GitLab' : 'GitHub'
export const providerRunner = (provider: Build['provider']) =>
  provider === 'gitlab' ? 'GitLab CI' : 'GitHub Actions'

// A mask in currentColor, so the mark follows the text color in both themes.
export function ProviderMark({ provider, size = 14 }: { provider: string; size?: number }) {
  return (
    <span
      aria-hidden="true"
      className="provider-mark"
      style={{
        width: size,
        height: size,
        maskImage: `url(/icons/${provider === 'gitlab' ? 'gitlab' : 'github'}.svg)`,
      }}
    />
  )
}

export function buildMethodLabel(build: Pick<Build, 'mode' | 'preset' | 'framework'>): string {
  if (build.mode === 'framework') return frameworkLabel(build.framework?.framework || '')
  if (build.mode === 'buildpacks') return `Buildpacks · ${build.preset}`
  return 'Dockerfile'
}

// The generated workflow must be committed before a configuration revision can build.
export function workflowState(build: Pick<Build, 'revision' | 'installed_revision'>): {
  status: string
  tone: StatusTone
  installed: boolean
} {
  if (build.installed_revision === build.revision)
    return { status: 'Workflow installed', tone: 'success', installed: true }
  if (build.installed_revision > 0)
    return { status: 'Workflow update required', tone: 'warning', installed: false }
  return { status: 'Workflow not installed', tone: 'warning', installed: false }
}

// Provider states we do not recognise are shown as reported, never as progress.
export function runState(run: Pick<Run, 'status' | 'conclusion'>): [string, StatusTone] {
  if (run.conclusion === 'success') return ['Succeeded', 'success']
  if (run.conclusion === 'timed_out') return ['Timed out', 'error']
  if (run.conclusion === 'failure' || run.status === 'failed') return ['Failed', 'error']
  if (run.conclusion === 'cancelled' || run.status === 'cancelled') return ['Cancelled', 'neutral']
  if (run.status === 'in_progress') return ['Building', 'running']
  if (run.status === 'dispatching') return ['Dispatching', 'running']
  if (run.status === 'cancelling') return ['Cancelling', 'running']
  if (run.status === 'queued') return ['Queued', 'neutral']
  if (run.status === 'dispatch_unknown') return ['Dispatch not confirmed', 'warning']
  const raw = (run.conclusion || run.status || 'unknown').replaceAll('_', ' ')
  return [raw.charAt(0).toUpperCase() + raw.slice(1), 'neutral']
}

export function StateLabel({
  label,
  tone,
  small = true,
}: {
  label: string
  tone: StatusTone
  small?: boolean
}) {
  return (
    <HatchStatus tone={tone} className={`hako-status ${small ? 'hako-status-small' : ''}`}>
      {label}
    </HatchStatus>
  )
}

export function RunStatus({ run, small = true }: { run: Run; small?: boolean }) {
  const [label, tone] = runState(run)
  return <StateLabel label={label} tone={tone} small={small} />
}

export function WorkflowStatus({ build, small = true }: { build: Build; small?: boolean }) {
  const state = workflowState(build)
  return <StateLabel label={state.status} tone={state.tone} small={small} />
}
