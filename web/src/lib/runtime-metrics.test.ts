import assert from 'node:assert/strict'
import test from 'node:test'
import { metricSampleAge, retainMetricSample, type MetricSample } from './runtime-metrics.ts'

const metric = (seconds: number) => ({
  available: true,
  sampled_at: new Date(Date.UTC(2026, 8, 12, 9, 0, seconds)).toISOString(),
  cpu_millicores: seconds,
  memory_bytes: 1024,
})

test('runtime charts retain a bounded history of complete, ordered source samples', () => {
  let samples: MetricSample[] = []
  for (let second = 0; second < 50; second++) samples = retainMetricSample(samples, metric(second))
  assert.equal(samples.length, 24)
  assert.equal(samples[0].at, metric(26).sampled_at)
  for (const invalid of [
    metric(49),
    metric(20),
    { ...metric(50), available: false },
    { ...metric(50), sampled_at: undefined },
    { ...metric(50), sampled_at: 'invalid' },
    { ...metric(50), cpu_millicores: NaN },
    { ...metric(50), memory_bytes: -1 },
    { ...metric(50), cpu_millicores: undefined },
  ])
    assert.equal(retainMetricSample(samples, invalid), samples)
  assert.equal(retainMetricSample([], metric(0))[0].cpu, 0)
})

test('metric freshness ages while paused and does not rely on browser and server clocks agreeing', () => {
  const receivedAt = 100_000
  assert.equal(
    metricSampleAge(metric(0).sampled_at, metric(15).sampled_at, receivedAt, 100_000),
    15_000,
  )
  assert.equal(
    metricSampleAge(metric(0).sampled_at, metric(15).sampled_at, receivedAt, 220_000),
    135_000,
  )
  assert.equal(metricSampleAge(undefined, metric(15).sampled_at, receivedAt, 100_000), null)
  assert.equal(metricSampleAge('invalid', metric(15).sampled_at, receivedAt, 100_000), null)
  assert.equal(
    metricSampleAge(metric(20).sampled_at, metric(0).sampled_at, receivedAt, 100_000),
    null,
  )
})
