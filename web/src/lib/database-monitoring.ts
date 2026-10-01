import type { components } from './api.generated'
import type { ManagedDatabase } from './databases'
import { databaseMetricAge } from './database-view'
import { metricsStaleAfter, type MetricSample } from './runtime-metrics'

export function databaseActivityCounter(engine: string, metric: components['schemas']['DatabaseEngineMetrics'] | undefined) {
  const transactions = engine === 'postgresql' || engine === 'oracle'
  const label = engine === 'redis' ? 'Commands processed' : engine === 'mongodb' ? 'Server operations' : transactions ? 'Transactions' : 'Server queries'
  return { label, value: transactions ? metric?.transactions : metric?.commands }
}

export function databaseHistorySamples(points: components['schemas']['DatabaseMetricPoint'][] = []): MetricSample[] {
  const samples: MetricSample[] = []
  let gap = true, revision = 0
  for (const point of points) {
    const source = point.resources
    const at = Date.parse(source?.sampled_at || '')
    const sourceAge = Date.parse(point.observed_at) - at
    if (!source?.available || !Number.isInteger(source.pods_expected) || source.pods_expected <= 0 || source.pods_sampled !== source.pods_expected || !Number.isFinite(sourceAge) || sourceAge < -10_000 || sourceAge > metricsStaleAfter || !Number.isFinite(source.cpu_millicores) || !Number.isFinite(source.memory_bytes) || (source.cpu_millicores ?? -1) < 0 || (source.memory_bytes ?? -1) < 0) {
      gap = true
      continue
    }
    const previous = samples.at(-1)
    if (previous && at <= Date.parse(previous.at)) continue
    samples.push({ at: source.sampled_at!, cpu: source.cpu_millicores!, memory: source.memory_bytes!, breakBefore: gap || revision !== point.revision || Boolean(previous && at - Date.parse(previous.at) > metricsStaleAfter) })
    gap = false
    revision = point.revision
  }
  return samples
}

export function databaseActivityFresh(d: ManagedDatabase, receivedAt: number, now: number) {
  const metric = d.observation.engine_metrics
  const observed = Date.parse(d.observation.observed_at)
  const age = databaseMetricAge(metric?.sampled_at, d.observation.observed_at, receivedAt, now)
  return Boolean(metric?.available && d.observation.revision === d.revision && observed <= now && now - observed <= 30_000 && age !== null && age <= metricsStaleAfter)
}
