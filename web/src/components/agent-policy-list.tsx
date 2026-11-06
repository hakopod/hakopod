import { Link } from '@tanstack/react-router'
import { Button } from './ui/button'
import { ErrorState, Loading } from './shared'
import { plural } from './operation-review'
import {
  SettingsEmpty,
  SettingsRow,
  SettingsRows,
  SettingsSection,
  SettingsState,
} from './settings-section'
import { statusLabels, useAgentPolicies, type AgentPolicy } from '../lib/agent-policies'

const statusTone: Record<AgentPolicy['status'], 'ok' | 'warn' | 'muted'> = {
  enforced: 'ok',
  monitor: 'warn',
  draft: 'muted',
}

export function AgentPolicyList() {
  const policies = useAgentPolicies()
  const data = policies.data
  const newPolicy = (
    <Button asChild>
      <Link to="/settings/agent-policies/new">New policy</Link>
    </Button>
  )
  const summary = data?.summary
  return (
    <SettingsSection
      title="Agent policies"
      help="Rules that block requests made with API keys, or hold them until a person approves the exact request. Monitor records what a policy would do without blocking."
      state={
        data && !data.licensed
          ? 'Requires Pro · existing policies are not applied until it is active'
          : summary &&
            summary.total > 0 && (
              <>
                {plural(summary.total, 'policy', 'policies')} · {summary.enforced} enforced ·{' '}
                {summary.monitor} monitor · {summary.draft} draft
                {summary.pending_approvals > 0 && (
                  <>
                    {' · '}
                    <Link to="/approvals" className="underline">
                      {plural(summary.pending_approvals, 'pending approval')}
                    </Link>
                  </>
                )}
              </>
            )
      }
      actions={data?.licensed && data.items.length > 0 && newPolicy}
    >
      {policies.isPending ? (
        <Loading />
      ) : policies.error || !data ? (
        <ErrorState error={policies.error} />
      ) : (
        <>
          {data.evaluating_since === null && (
            <p className="settings-section-state">No machine-key requests recorded yet.</p>
          )}
          {!data.items.length ? (
            <SettingsEmpty title="No agent policies" action={data.licensed && newPolicy} />
          ) : (
            <SettingsRows label="Agent policies">
              {data.items.map((policy) => (
                <SettingsRow
                  key={policy.id}
                  className="relative"
                  name={
                    <Link
                      to="/settings/agent-policies/$policyId"
                      params={{ policyId: policy.id }}
                      className="after:absolute after:inset-0 after:rounded focus-visible:outline-none focus-visible:after:outline-2 focus-visible:after:outline-offset-2 focus-visible:after:outline-current"
                    >
                      {policy.name}
                    </Link>
                  }
                  detail={
                    <span className="line-clamp-2 [overflow-wrap:anywhere]">
                      {policy.document?.description || policy.draft?.description}
                    </span>
                  }
                  status={
                    <SettingsState tone={statusTone[policy.status]}>
                      {statusLabels[policy.status]}
                    </SettingsState>
                  }
                  meta={[
                    (policy.draft ?? policy.document)?.then.effect === 'block'
                      ? 'Blocks'
                      : 'Needs approval',
                    policy.current_version > 0 && `v${policy.current_version}`,
                    policy.current_version > 0 && policy.draft && 'Unpublished changes',
                  ]
                    .filter(Boolean)
                    .join(' · ')}
                />
              ))}
            </SettingsRows>
          )}
        </>
      )}
    </SettingsSection>
  )
}
