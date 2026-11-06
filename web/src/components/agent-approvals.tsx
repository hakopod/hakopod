import { useEffect, useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { useQueryClient } from '@tanstack/react-query'
import {
  agentApprovalsKey,
  agentPoliciesKey,
  decideAgentApproval,
  useAgentApprovals,
  type AgentApproval,
} from '../lib/agent-policies'
import { ago } from '../lib/access-policy'
import { humanizeOperation } from '../lib/agent-policy-form'
import { APIError, message } from '../lib/api'
import { Button } from './ui/button'
import { Textarea } from './ui/textarea'
import { InspectorSheet } from './inspector-sheet'
import { Copy, ErrorState, Loading, Note, Status } from './shared'

function useNow() {
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 30000)
    return () => clearInterval(timer)
  }, [])
  return now
}

function expiresIn(at: string, now: number) {
  const minutes = Math.ceil((new Date(at).getTime() - now) / 60000)
  return minutes <= 0 ? 'Expired' : `Expires in ${minutes} min`
}

const approverLabel = (a: AgentApproval['approvers'][number]) =>
  a.kind === 'project_admins'
    ? 'Project administrators'
    : `${a.kind === 'team' ? 'Team' : 'Person'} ${a.id || ''}`.trim()

export function AgentApprovals() {
  const [tab, setTab] = useState<'pending' | 'decided'>('pending')
  const [selected, setSelected] = useState<AgentApproval | null>(null)
  return (
    <Tabs.Root value={tab} onValueChange={(value) => setTab(value as typeof tab)}>
      <Tabs.List className="tab-list" aria-label="Approval states">
        <Tabs.Trigger className="tab-trigger" value="pending">
          Pending
        </Tabs.Trigger>
        <Tabs.Trigger className="tab-trigger" value="decided">
          Decided
        </Tabs.Trigger>
      </Tabs.List>
      {(['pending', 'decided'] as const).map((state) => (
        <Tabs.Content key={state} value={state} className="tab-content">
          <ApprovalList state={state} onOpen={setSelected} />
        </Tabs.Content>
      ))}
      {selected && <ApprovalSheet approval={selected} onClose={() => setSelected(null)} />}
    </Tabs.Root>
  )
}

function useDecision(a: AgentApproval, onDone?: () => void) {
  const cache = useQueryClient()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const decide = async (approve: boolean, reason = '') => {
    setBusy(true)
    setError('')
    try {
      await decideAgentApproval(a.id, approve, reason.trim())
      await Promise.all(
        [agentApprovalsKey('pending'), agentApprovalsKey('decided'), agentPoliciesKey].map(
          (queryKey) => cache.invalidateQueries({ queryKey }),
        ),
      )
      onDone?.()
    } catch (err) {
      setError(
        err instanceof APIError && err.status === 409
          ? 'This request was already decided or has expired.'
          : message(err),
      )
    } finally {
      setBusy(false)
    }
  }
  return { decide, error, busy }
}

// Approvals store the escaped path the gate hashed; show it readable.
function readablePath(path: string) {
  try {
    return decodeURI(path)
  } catch {
    return path
  }
}

const scopeOf = (a: AgentApproval) => [a.project, a.environment].filter(Boolean).join(' / ')

// "deploy-agent wants to Create deployment in storefront / production"
function Request({ a }: { a: AgentApproval }) {
  const scope = scopeOf(a)
  return (
    <span className="text-sm leading-6">
      <strong className="font-medium">{a.key_name}</strong>
      {a.state === 'pending' ? ' wants to ' : ' asked to '}
      <strong className="font-medium" title={a.operation}>
        {humanizeOperation(a.operation)}
      </strong>
      {scope && (
        <>
          {' in '}
          <strong className="font-medium">{scope}</strong>
        </>
      )}
    </span>
  )
}

function Expiry({ at, now }: { at: string; now: number }) {
  const minutes = Math.ceil((new Date(at).getTime() - now) / 60000)
  const urgent = minutes <= 5
  return (
    <span
      className={`rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap ${urgent ? 'bg-amber-500/10 text-amber-700 dark:text-amber-300' : 'bg-[var(--surface-2)] text-[var(--muted-foreground)]'}`}
    >
      {expiresIn(at, now)}
    </span>
  )
}

function ApprovalRow({
  a,
  now,
  onOpen,
}: {
  a: AgentApproval
  now: number
  onOpen: (approval: AgentApproval) => void
}) {
  const { decide, error, busy } = useDecision(a)
  const pending = a.state === 'pending'
  return (
    <li className="grid gap-3 px-4 py-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
      <button
        type="button"
        className="grid min-w-0 gap-0.5 text-left focus-visible:outline-2 focus-visible:outline-offset-2"
        onClick={() => onOpen(a)}
      >
        <Request a={a} />
        <span className="truncate font-mono text-xs text-[var(--muted-foreground)]">
          {a.method} {readablePath(a.path)}
        </span>
        <span className="truncate text-xs text-[var(--muted-foreground)]">
          {a.identity_name}'s key · {ago(a.created_at)} · {a.policy_name} v{a.policy_version}
        </span>
      </button>
      <div className="flex flex-wrap items-center gap-2 sm:justify-end">
        {pending ? (
          <>
            <Expiry at={a.expires_at} now={now} />
            {a.can_decide ? (
              <>
                <Button size="sm" disabled={busy} onClick={() => decide(false)}>
                  Deny
                </Button>
                <Button size="sm" variant="primary" disabled={busy} onClick={() => decide(true)}>
                  Approve once
                </Button>
              </>
            ) : (
              <span className="text-xs text-[var(--muted-foreground)]">
                Another approver decides
              </span>
            )}
          </>
        ) : (
          <span className="grid justify-items-start gap-0.5 text-xs text-[var(--muted-foreground)] sm:justify-items-end">
            <Status value={a.state} small />
            {a.decided_by_name && (
              <span className="max-w-72 truncate">
                by {a.decided_by_name}
                {a.reason && ` · ${a.reason}`}
              </span>
            )}
          </span>
        )}
      </div>
      {error && (
        <p role="alert" className="text-sm text-[var(--error-text)] sm:col-span-2">
          {error}
        </p>
      )}
    </li>
  )
}

function ApprovalList({
  state,
  onOpen,
}: {
  state: 'pending' | 'decided'
  onOpen: (approval: AgentApproval) => void
}) {
  const approvals = useAgentApprovals(state)
  const now = useNow()
  if (approvals.isPending) return <Loading />
  if (approvals.error instanceof APIError && approvals.error.code === 'license_required')
    return <Note>Requires Pro. Agent policies have no effect until Pro is active.</Note>
  if (approvals.error) return <ErrorState error={approvals.error} retry={approvals.refetch} />
  const items = approvals.data.items
  if (!items.length)
    return (
      <div className="grid justify-items-start gap-1 py-8">
        <p className="text-sm font-medium">
          {state === 'pending' ? 'Nothing is waiting for approval' : 'No decided requests yet'}
        </p>
        <p className="text-sm text-[var(--muted-foreground)]">
          {state === 'pending'
            ? 'Requests held by an agent policy appear here.'
            : 'Approved, denied and expired requests appear here.'}
        </p>
      </div>
    )
  return (
    <ul className="divide-y divide-[var(--hairline)] overflow-hidden rounded-[var(--radius-card)] border border-[var(--hairline)] bg-[var(--card)]">
      {items.map((a) => (
        <ApprovalRow key={a.id} a={a} now={now} onOpen={onOpen} />
      ))}
    </ul>
  )
}

function ApprovalSheet({ approval: a, onClose }: { approval: AgentApproval; onClose: () => void }) {
  const [reason, setReason] = useState('')
  const { decide, error, busy } = useDecision(a, onClose)
  const decidable = a.state === 'pending'
  const facts: [string, React.ReactNode][] = [
    ['Key', a.key_name],
    ['Requested by', a.identity_name],
    ['Operation', <span className="font-mono">{a.operation}</span>],
    ['Category', a.category],
    [
      'Request',
      <span className="font-mono break-all">{`${a.method} ${readablePath(a.path)}`}</span>,
    ],
    ['Scope', [a.project, a.environment].filter(Boolean).join(' / ') || 'Installation'],
    ['Policy', `${a.policy_name} v${a.policy_version}`],
    [
      'Digest',
      <span className="flex min-w-0 items-center gap-1">
        <span className="truncate font-mono">{a.digest}</span>
        <Copy value={a.digest} label="Copy digest" iconOnly />
      </span>,
    ],
    ['Approvers', a.approvers.map(approverLabel).join(', ')],
    ['Requested', new Date(a.created_at).toLocaleString()],
    ['Expires', new Date(a.expires_at).toLocaleString()],
    ...(a.decided_at
      ? ([['Decided', `${a.decided_by_name} · ${new Date(a.decided_at).toLocaleString()}`]] as [
          string,
          string,
        ][])
      : []),
    ...(a.reason ? ([['Reason', a.reason]] as [string, string][]) : []),
    ...(a.consumed_at
      ? ([['Used', new Date(a.consumed_at).toLocaleString()]] as [string, string][])
      : []),
  ]
  return (
    <InspectorSheet
      open
      onOpenChange={(open) => !open && onClose()}
      title={`${a.key_name} · ${humanizeOperation(a.operation)}`}
      kind="approval"
      description="An approval lets this exact request run once before it expires."
      state={<Status value={a.state} small />}
      footer={
        decidable && (
          <>
            <Button
              variant="outline"
              disabled={!a.can_decide || busy}
              onClick={() => decide(false, reason)}
            >
              Deny
            </Button>
            <Button
              variant="primary"
              disabled={!a.can_decide || busy}
              onClick={() => decide(true, reason)}
            >
              Approve once
            </Button>
          </>
        )
      }
    >
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">
        {facts.map(([label, value]) => (
          <div key={label} className="contents">
            <dt className="text-muted-foreground">{label}</dt>
            <dd className="min-w-0 [overflow-wrap:anywhere]">{value}</dd>
          </div>
        ))}
      </dl>
      {decidable &&
        (a.can_decide ? (
          <label className="mt-4 grid gap-1 text-sm">
            Reason (optional)
            <Textarea
              value={reason}
              maxLength={500}
              rows={3}
              onChange={(event) => setReason(event.target.value)}
            />
          </label>
        ) : (
          <p className="mt-4 text-sm text-[var(--muted-foreground)]">
            You can't decide this request: it was made with your own key, or you are not one of its
            approvers.
          </p>
        ))}
      {error && (
        <p role="alert" className="mt-3 text-sm text-destructive">
          {error}
        </p>
      )}
    </InspectorSheet>
  )
}
