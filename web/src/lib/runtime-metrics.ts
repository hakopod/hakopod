export const retainedMetricSamples = 24
export const metricsStaleAfter = 120_000

export type MetricSample = { at: string; cpu: number; memory: number }
type Metrics = {
  available: boolean
  sampled_at?: string
  cpu_millicores?: number
  memory_bytes?: number
}

export function retainMetricSample(previous: MetricSample[], metrics?: Metrics): MetricSample[] {
  if (!metrics?.available || !metrics.sampled_at) return previous
  const at = Date.parse(metrics.sampled_at)
  const cpu = metrics.cpu_millicores
  const memory = metrics.memory_bytes
  if (
    !Number.isFinite(at) ||
    cpu === undefined ||
    memory === undefined ||
    !Number.isFinite(cpu) ||
    !Number.isFinite(memory) ||
    cpu < 0 ||
    memory < 0 ||
    (previous.length > 0 && at <= Date.parse(previous[previous.length - 1].at))
  )
    return previous
  return [...previous.slice(-(retainedMetricSamples - 1)), { at: metrics.sampled_at, cpu, memory }]
}

export function metricSampleAge(
  sampledAt: string | undefined,
  observedAt: string | undefined,
  receivedAt: number,
  now: number,
): number | null {
  if (!sampledAt || !observedAt || !receivedAt) return null
  const sourceAge = Date.parse(observedAt) - Date.parse(sampledAt)
  if (!Number.isFinite(sourceAge) || sourceAge < -10_000) return null
  // Both source timestamps use the server clock; browser clock skew cannot make a sample fresh.
  return Math.max(0, sourceAge) + Math.max(0, now - receivedAt)
}
