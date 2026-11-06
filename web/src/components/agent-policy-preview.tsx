import { useEffect, useState } from 'react'
import { SelectField } from './ui/select'
import { HeadingHelp } from './shared'
import { humanizeOperation } from '../lib/agent-policy-form'
import { message, relative, timestamp } from '../lib/api'
import {
  previewAgentPolicy,
  type AgentPolicyDocument,
  type AgentPolicyPreview,
  type AgentPolicySample,
} from '../lib/agent-policies'

export const decisionLabels: Record<AgentPolicySample['decision'], string> = {
  allow: 'Allowed',
  block: 'Blocked',
  require_approval: 'Needs approval',
  approved: 'Approved',
  exception: 'Allowed during alarm',
  monitor_block: 'Would block',
  monitor_approval: 'Would need approval',
}

export function DecisionChip({ decision }: { decision: AgentPolicySample['decision'] }) {
  const tone =
    decision === 'block' || decision === 'monitor_block'
      ? 'bg-red-500/10 text-red-700 dark:text-red-300'
      : decision === 'require_approval' || decision === 'monitor_approval'
        ? 'bg-amber-500/10 text-amber-700 dark:text-amber-300'
        : decision === 'exception'
          ? 'bg-sky-500/10 text-sky-700 dark:text-sky-300'
          : 'bg-[var(--surface-2)] text-[var(--muted-foreground)]'
  return (
    <span
      className={`shrink-0 rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap ${tone}`}
    >
      {decisionLabels[decision]}
    </span>
  )
}

const ranges = [
  { value: '1', label: 'Last 24 hours' },
  { value: '7', label: 'Last 7 days' },
  { value: '30', label: 'Last 30 days' },
]

export function AgentPolicyImpact({
  document,
  valid,
}: {
  document: AgentPolicyDocument
  valid: boolean
}) {
  const [days, setDays] = useState('7')
  const [preview, setPreview] = useState<AgentPolicyPreview | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const key = JSON.stringify(document)

  useEffect(() => {
    if (!valid) return
    const controller = new AbortController()
    const timer = setTimeout(() => {
      setLoading(true)
      previewAgentPolicy(JSON.parse(key), Number(days), controller.signal)
        .then((result) => {
          setPreview(result)
          setError('')
        })
        .catch((reason) => {
          if (!controller.signal.aborted) setError(message(reason))
        })
        .finally(() => {
          if (!controller.signal.aborted) setLoading(false)
        })
    }, 600)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [key, days, valid])

  const since = preview?.evaluating_since
  const partial = since && Date.now() - new Date(since).getTime() < Number(days) * 86400000
  const total = preview ? preview.require_approval + preview.blocked + preview.exceptions : 0

  const muted = 'text-sm text-[var(--muted-foreground)]'
  return (
    <section aria-labelledby="impact-heading" aria-busy={loading} className="grid gap-4">
      <div className="grid gap-2">
        <div className="flex items-center gap-1.5">
          <h2 id="impact-heading" className="text-sm font-medium">
            Impact
          </h2>
          <HeadingHelp title="Impact">
            Recorded API key requests replayed against your edits. Nothing here is estimated.
          </HeadingHelp>
        </div>
        <SelectField label="Range" value={days} onValueChange={setDays} options={ranges} />
      </div>
      {!valid ? (
        <p className={muted}>Complete the policy to see which requests it would catch.</p>
      ) : error ? (
        <p role="alert" className="text-sm text-[var(--error-text)]">
          {error}
        </p>
      ) : !preview ? (
        <p className={muted}>Checking recorded requests…</p>
      ) : !since ? (
        <p className={muted}>
          No API key requests have been recorded yet. The impact appears here once keys are used.
        </p>
      ) : (
        <>
          <div className="grid gap-0.5">
            <span className="text-[32px] leading-10 font-medium tabular-nums">
              {preview.matched}
            </span>
            <span className={muted}>
              of {preview.evaluated} recorded {preview.evaluated === 1 ? 'request' : 'requests'}{' '}
              would match
            </span>
            {partial && (
              <span className="text-xs text-[var(--muted-foreground)]">
                Recording started {timestamp(since)}.
              </span>
            )}
          </div>
          {total > 0 && (
            <div className="flex h-2 gap-0.5 overflow-hidden rounded-full" aria-hidden>
              {preview.require_approval > 0 && (
                <span
                  className="rounded-full bg-amber-500"
                  style={{ flexGrow: preview.require_approval }}
                />
              )}
              {preview.blocked > 0 && (
                <span className="rounded-full bg-red-500" style={{ flexGrow: preview.blocked }} />
              )}
              {preview.exceptions > 0 && (
                <span
                  className="rounded-full bg-sky-500"
                  style={{ flexGrow: preview.exceptions }}
                />
              )}
            </div>
          )}
          <dl className="grid gap-1.5 text-sm">
            {[
              ['bg-amber-500', 'Would wait for approval', preview.require_approval],
              ['bg-red-500', 'Would be blocked', preview.blocked],
              ['bg-sky-500', 'Allowed during an alarm', preview.exceptions],
            ].map(([tone, label, count]) => (
              <div key={label as string} className="flex items-center gap-2">
                <span className={`size-2 rounded-full ${tone}`} aria-hidden />
                <dt className="flex-1">{label}</dt>
                <dd className="tabular-nums">{count}</dd>
              </div>
            ))}
          </dl>
          {preview.samples.length > 0 && (
            <div className="grid gap-2">
              <h3 className="text-xs font-medium text-[var(--muted-foreground)]">Recent matches</h3>
              <ul className="grid divide-y divide-[var(--hairline)] border-y border-[var(--hairline)]">
                {preview.samples.slice(0, 8).map((sample, index) => (
                  <li key={`${sample.observed_at}-${index}`} className="grid gap-1 py-2.5">
                    <div className="flex items-center justify-between gap-2">
                      <span className="min-w-0 truncate text-sm font-medium">
                        {sample.key_name}
                      </span>
                      <DecisionChip decision={sample.decision} />
                    </div>
                    <div className="flex min-w-0 items-center gap-2 text-xs text-[var(--muted-foreground)]">
                      <span className="min-w-0 truncate" title={sample.operation}>
                        {humanizeOperation(sample.operation)}
                      </span>
                      <span aria-hidden>·</span>
                      <span className="min-w-0 shrink truncate">
                        {[sample.project, sample.environment].filter(Boolean).join(' / ')}
                      </span>
                      <span className="ml-auto shrink-0">{relative(sample.observed_at)}</span>
                    </div>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </>
      )}
    </section>
  )
}
