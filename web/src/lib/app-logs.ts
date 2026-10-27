import type { components } from './api.generated'

export type AppLogSource = components['schemas']['AppLogSource']
export const unavailablePrefix = 'Log history is not available on this installation.'
const reasons: Record<string, string> = {
  completed: 'Completed',
  error: 'Exited with error',
  oom_killed: 'OOM killed',
  evicted: 'Evicted',
  replaced: 'Replaced by rollout',
  deleted: 'Deleted',
}
export const endReason = (value: string) => reasons[value] || ''

export function ago(date: string | null | undefined, now = Date.now()) {
  if (!date) return ''
  const s = Math.max(0, now - Date.parse(date)) / 1000
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

export function podLabel(item: AppLogSource, now = Date.now()) {
  if (item.current || !item.ended_at) return `${item.pod_name} · running`
  const reason = endReason(item.end_reason)
  return `${item.pod_name} · ended ${ago(item.ended_at, now)}${reason ? ` · ${reason}` : ''}`
}

// All pods, then current pods, then past pods newest first.
export function podOptions(items: readonly AppLogSource[], now = Date.now()) {
  const current = items.filter((item) => item.current || !item.ended_at)
  const past = items
    .filter((item) => !current.includes(item))
    .sort((a, b) => Date.parse(b.ended_at || '') - Date.parse(a.ended_at || ''))
  return [
    { value: '', label: 'All pods' },
    ...[...current, ...past].map((item) => ({
      value: item.pod_uid,
      label: podLabel(item, now),
    })),
  ]
}

const windows: [number, string][] = [
  [900, 'Last 15 minutes'],
  [3600, 'Last hour'],
  [21600, 'Last 6 hours'],
  [86400, 'Last 24 hours'],
  [259200, 'Last 3 days'],
  [604800, 'Last 7 days'],
]
// Options longer than retention are hidden; Custom is always offered once history is on.
export function timeOptions(retentionDays: number | undefined, history: boolean) {
  const max = history && retentionDays ? retentionDays * 86400 : 86400
  const list = windows.filter(([seconds]) => seconds <= max)
  return [
    ...list.map(([seconds, label]) => ({ value: String(seconds), label })),
    ...(history ? [{ value: 'custom', label: 'Custom' }] : []),
  ]
}

export function validateCustom(
  from: string,
  to: string,
  retentionDays: number | undefined,
  now = Date.now(),
) {
  const a = Date.parse(from)
  const b = Date.parse(to)
  if (Number.isNaN(a) || Number.isNaN(b)) return 'Enter both a start and an end time.'
  if (b <= a) return 'The end time must be after the start time.'
  if (retentionDays && a < now - retentionDays * 86400000)
    return `The start time is older than the ${retentionDays}-day retention window.`
  return ''
}

// datetime-local strings are local time; the API gets UTC.
export const utc = (local: string) => new Date(local).toISOString()

export function validateLogSettings(retentionDays: string, maxMiB: string) {
  const whole = (v: string) => /^\d+$/.test(v.trim())
  const days = Number(retentionDays)
  const mib = Number(maxMiB)
  return {
    retention:
      whole(retentionDays) && days >= 1 && days <= 30
        ? ''
        : 'Retention must be a whole number of days from 1 to 30.',
    size:
      whole(maxMiB) && mib >= 64 && mib <= 4096
        ? ''
        : 'Maximum size must be a whole number of MiB from 64 to 4096.',
  }
}
export const retentionNote = (days: number, mib: number) =>
  `Hakopod keeps application logs for ${days} days, up to ${mib} MiB per application.`
