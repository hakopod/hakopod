import type { components } from '../lib/api.generated'
import { Button } from './ui/button'
import { Note } from './shared'

type Plan = components['schemas']['DatabaseCapacityPlan']

const cpu = (value: number) =>
  `${(value / 1000).toLocaleString(undefined, { maximumFractionDigits: 3 })} CPU`
const memory = (value: number) =>
  `${(value / 2 ** 30).toLocaleString(undefined, { maximumFractionDigits: 2 })} GiB`
const count = (value: number, singular: string) =>
  `${value.toLocaleString()} ${singular}${value === 1 ? '' : 's'}`

export function DatabaseCapacityPlan({ plan, onApply }: { plan: Plan; onApply: () => void }) {
  const recommendation = plan.recommendation
  return (
    <section className="grid gap-3" aria-labelledby="database-capacity-plan-title">
      <div>
        <h3 id="database-capacity-plan-title">Resource recommendation</h3>
        <p className="field-help">
          Advisory policy version {recommendation.policy_version} · {recommendation.confidence}{' '}
          confidence
        </p>
      </div>
      <dl className="db-create-facts">
        <div>
          <dt>Configured per member</dt>
          <dd>
            {cpu(plan.per_member.cpu_milli)} · {memory(plan.per_member.memory_bytes)} ·{' '}
            {plan.per_member.storage_gib} GiB storage
          </dd>
        </div>
        <div>
          <dt>Recommended per member</dt>
          <dd>
            {cpu(recommendation.cpu_milli)} · {memory(recommendation.memory_bytes)}
          </dd>
        </div>
        <div>
          <dt>Total recovery allocation</dt>
          <dd>
            {cpu(plan.effective_allocation.cpu_milli)} ·{' '}
            {memory(plan.effective_allocation.memory_bytes)} ·{' '}
            {plan.effective_allocation.storage_gib} GiB storage
          </dd>
        </div>
        <div>
          <dt>Connected workload</dt>
          <dd>
            {count(plan.usage.connected_applications, 'application')} ·{' '}
            {count(plan.usage.connected_services, 'service')}
            {plan.usage.connections_truncated ? ' (bounded result)' : ''}
          </dd>
        </div>
        <div>
          <dt>Usage evidence</dt>
          <dd>
            {plan.usage.current_available ? 'Current sample available' : 'No current sample'} ·{' '}
            {count(plan.usage.samples_24h, 'sample')} in 24 hours
          </dd>
        </div>
        <div>
          <dt>Server capacity</dt>
          <dd>
            {plan.capacity.known
              ? `${cpu(plan.capacity.after_plan.cpu_milli)} · ${memory(plan.capacity.after_plan.memory_bytes)} · ${plan.capacity.after_plan.storage_gib} GiB after this plan`
              : plan.capacity.reason || 'Unknown'}
          </dd>
        </div>
      </dl>
      {recommendation.reasons.map((reason) => (
        <Note key={reason}>{reason}</Note>
      ))}
      {(plan.per_member.cpu_milli !== recommendation.cpu_milli ||
        plan.per_member.memory_bytes !== recommendation.memory_bytes) && (
        <Button type="button" onClick={onApply}>
          Apply recommendation
        </Button>
      )}
    </section>
  )
}
