// Pure helpers for the application workspace: what needs attention, how to say the
// application's health in one sentence, and what changed between two revisions.
import type { Spec } from './types'

/** Lower sorts first: failing services lead the overview. */
export function attentionRank(status: string) {
  if (status === 'failed') return 0
  if (status === 'blocked') return 1
  if (['missing', 'pending', 'deploying', 'progressing', 'terminating'].includes(status)) return 2
  if (['stale', 'unknown', 'not observed'].includes(status)) return 3
  return 4
}

export const needsAttention = (status: string) => attentionRank(status) <= 1

/** A plain phrase for what is wrong, from the observed status and its message. */
export function issuePhrase(status: string, message = '') {
  if (/CrashLoopBackOff|container exited/i.test(message)) return 'crash-looping'
  if (/OOMKilled/i.test(message)) return 'running out of memory'
  if (/ImagePullBackOff|ErrImagePull|InvalidImageName/i.test(message))
    return 'unable to pull its image'
  if (/Unschedulable|FailedScheduling|Insufficient (cpu|memory)/i.test(message))
    return 'waiting for capacity'
  if (/readiness check has not passed/i.test(message)) return 'not passing its readiness check'
  return status === 'blocked' ? 'blocked' : status
}

export type ServiceHealthSummary = { name: string; status: string; message?: string }

/** "1 of 3 services failing · reports crash-looping · revision 2 failed" */
export function healthSentence(
  services: ServiceHealthSummary[],
  latest?: { revision: number; status: string },
) {
  const parts: string[] = []
  const total = services.length
  const failing = services.filter((s) => needsAttention(s.status))
  const unobserved = services.filter((s) => attentionRank(s.status) === 3)
  const settling = services.filter((s) => attentionRank(s.status) === 2)
  if (!total) parts.push('No services')
  else if (failing.length) {
    parts.push(`${failing.length} of ${total} ${total === 1 ? 'service' : 'services'} failing`)
    failing.slice(0, 2).forEach((s) => parts.push(`${s.name} ${issuePhrase(s.status, s.message)}`))
    if (failing.length > 2) parts.push(`${failing.length - 2} more`)
  } else if (unobserved.length === total) parts.push('Health not observed yet')
  else if (settling.length) parts.push(`${settling.length} of ${total} starting`)
  else parts.push(total === 1 ? 'The service is running' : `All ${total} services running`)
  if (latest && ['failed', 'running', 'queued', 'cancelled'].includes(latest.status))
    parts.push(
      latest.status === 'failed'
        ? `revision ${latest.revision} failed`
        : latest.status === 'cancelled'
          ? `revision ${latest.revision} cancelled`
          : `revision ${latest.revision} deploying`,
    )
  return parts
}

/** "docker.io/library/python:3.13.15-alpine@sha256:…" → "python:3.13.15-alpine" */
export function imageName(image = '') {
  const reference = image.split('@')[0]
  return reference.split('/').pop() || reference
}

/** Short, human summary of what one revision changed compared with the one before. */
export function specChanges(previous: Spec | undefined, next: Spec): string[] {
  const after = next.services || {}
  if (!previous) {
    const count = Object.keys(after).length
    return [`First revision · ${count} ${count === 1 ? 'service' : 'services'}`]
  }
  const before = previous.services || {}
  const changes: string[] = []
  for (const name of Object.keys(after)) if (!before[name]) changes.push(`+${name}`)
  for (const name of Object.keys(before)) if (!after[name]) changes.push(`−${name}`)
  for (const [name, service] of Object.entries(after)) {
    const old = before[name]
    if (!old) continue
    if (old.image !== service.image) {
      const [a, b] = [imageName(old.image), imageName(service.image)]
      changes.push(a === b ? `${name} image re-pinned` : `${name} image ${a} → ${b}`)
    }
    if ((old.replicas ?? 1) !== (service.replicas ?? 1))
      changes.push(`${name} replicas ${old.replicas ?? 1} → ${service.replicas ?? 1}`)
    if ((old.size || 'small') !== (service.size || 'small'))
      changes.push(`${name} size ${old.size || 'small'} → ${service.size || 'small'}`)
    if (Boolean(old.public) !== Boolean(service.public))
      changes.push(`${name} ${service.public ? 'made public' : 'made private'}`)
    const [oldEnv, newEnv] = [Object.keys(old.env || {}), Object.keys(service.env || {})]
    const added = newEnv.filter((key) => !oldEnv.includes(key)).length
    const removed = oldEnv.filter((key) => !newEnv.includes(key)).length
    const edited = newEnv.filter(
      (key) => oldEnv.includes(key) && old.env?.[key] !== service.env?.[key],
    ).length
    if (added || removed || edited)
      changes.push(
        `${name} variables ${[
          added && `+${added}`,
          removed && `−${removed}`,
          edited && `~${edited}`,
        ]
          .filter(Boolean)
          .join(' ')}`,
      )
    if (
      JSON.stringify(old.command || []) !== JSON.stringify(service.command || []) ||
      JSON.stringify(old.args || []) !== JSON.stringify(service.args || [])
    )
      changes.push(`${name} command changed`)
  }
  const [oldDomains, newDomains] = [
    Object.keys(previous.domains || {}),
    Object.keys(next.domains || {}),
  ]
  const domainDelta = newDomains.length - oldDomains.length
  if (domainDelta || newDomains.some((d) => !oldDomains.includes(d)))
    changes.push(
      domainDelta >= 0 ? `domains +${Math.max(domainDelta, 1)}` : `domains ${domainDelta}`,
    )
  return changes.length ? changes : ['No configuration change']
}

/** The most useful single line from a service log tail: the last error-looking line. */
export function lastErrorLine(text: string) {
  const lines = text
    .split('\n')
    // Drop the RFC 3339 timestamp Kubernetes prefixes when timestamps are requested.
    .map((line) => line.replace(/^\d{4}-\d\d-\d\dT[\d:.]+Z\s+/, '').trim())
    // Platform placeholders such as “[incomplete log line withheld]” are not output.
    .filter((line) => line && !/^\[[^\]]*\]$/.test(line))
  const error = [...lines]
    .reverse()
    .find((line) =>
      /error|exception|fatal|panic|traceback|refused|denied|not set|failed|cannot|unable/i.test(
        line,
      ),
    )
  const line = error || lines[lines.length - 1] || ''
  return line.length > 200 ? `${line.slice(0, 199)}…` : line
}
