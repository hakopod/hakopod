import { useEffect, useState } from 'react'
import type { Node } from '../lib/types'
import { message } from '../lib/api'
import {
  metricSampleAge,
  metricsStaleAfter,
  retainMetricSample,
  retainedMetricSamples,
  type MetricSample,
} from '../lib/runtime-metrics'
import { Button } from './ui/button'
import { Icon } from './icons'
import { Badge } from './ui/surfaces'
import { Note } from './shared'

const sampleTime = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
})

function timestamp(value?: string) {
  const time = Date.parse(value || '')
  return Number.isFinite(time) ? sampleTime.format(time) : 'Unavailable'
}

function validUsage(value?: number): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}

function formatUsage(value: number | undefined, field: 'cpu' | 'memory') {
  if (!validUsage(value)) return 'Unavailable'
  if (field === 'cpu')
    return `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })} mCPU`
  return value >= 1024 ** 3
    ? `${(value / 1024 ** 3).toFixed(2)} GiB`
    : `${(value / 1024 ** 2).toFixed(1)} MiB`
}

export function NodeMetrics({
  metrics,
  cpuCapacity,
  memoryCapacity,
  observedAt,
  receivedAt,
  paused,
  visible,
  fetching,
  error,
  onPause,
  onProbe,
}: {
  metrics: Node['metrics']
  cpuCapacity: number
  memoryCapacity: number
  observedAt?: string
  receivedAt: number
  paused: boolean
  visible: boolean
  fetching: boolean
  error: unknown
  onPause: () => void
  onProbe: () => void
}) {
  const [samples, setSamples] = useState<MetricSample[]>([])
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    setSamples((previous) => retainMetricSample(previous, metrics))
  }, [metrics])
  useEffect(() => {
    if (!visible) return
    setNow(Date.now())
    const timer = setInterval(() => setNow(Date.now()), 5000)
    return () => clearInterval(timer)
  }, [visible])
  const sampleAge = metricSampleAge(metrics.sampled_at, observedAt, receivedAt, now)
  const available =
    metrics.available && validUsage(metrics.cpu_millicores) && validUsage(metrics.memory_bytes)
  const stale = sampleAge !== null && sampleAge > metricsStaleAfter
  const freshness = error
    ? 'Check failed'
    : !available
      ? 'No sample'
      : sampleAge === null
        ? 'Freshness unknown'
        : stale
          ? 'Stale sample'
          : paused || !visible
            ? 'Paused'
            : 'Live'
  return (
    <section className="node-runtime" aria-label="Node resource usage">
      <div className="node-runtime-heading">
        <div className="node-runtime-title">
          <h3>Resource usage</h3>
          <Badge>{freshness}</Badge>
        </div>
        <div className="node-runtime-controls">
          <Button variant="ghost" size="sm" onClick={onPause}>
            <Icon name={paused ? 'play' : 'pause'} size={14} />
            {paused ? 'Resume live' : 'Pause live'}
          </Button>
          <Button variant="ghost" size="sm" onClick={onProbe} disabled={fetching || !visible}>
            <Icon name="refresh" size={14} className={fetching ? 'spin' : ''} />
            {fetching ? 'Probing…' : 'Probe now'}
          </Button>
        </div>
      </div>
      {error ? (
        <p className="node-runtime-error" role="alert">
          {message(error)}
        </p>
      ) : null}
      <div className="node-runtime-values">
        <NodeMetric
          label="CPU"
          field="cpu"
          used={available ? metrics.cpu_millicores : undefined}
          total={cpuCapacity}
          samples={samples}
        />
        <NodeMetric
          label="Memory"
          field="memory"
          used={available ? metrics.memory_bytes : undefined}
          total={memoryCapacity}
          samples={samples}
        />
      </div>
      <dl className="node-runtime-freshness">
        <div>
          <dt>Source sample</dt>
          <dd>
            {metrics.sampled_at ? (
              <time dateTime={metrics.sampled_at}>{timestamp(metrics.sampled_at)}</time>
            ) : (
              'Unavailable'
            )}
            {sampleAge !== null && ` · ${Math.floor(sampleAge / 1000)}s old`}
          </dd>
        </div>
        <div>
          <dt>Last checked</dt>
          <dd>
            {observedAt ? (
              <time dateTime={observedAt}>{timestamp(observedAt)}</time>
            ) : (
              'Not checked'
            )}
          </dd>
        </div>
      </dl>
      <p className="node-runtime-help">
        Every 15s while visible · {samples.length} / {retainedMetricSamples} source samples. Probe
        reads the latest cluster sample.
      </p>
      {!available && <Note>{metrics.reason || 'Metrics are unavailable for this node.'}</Note>}
    </section>
  )
}

function NodeMetric({
  label,
  field,
  used,
  total,
  samples,
}: {
  label: string
  field: 'cpu' | 'memory'
  used?: number
  total: number
  samples: MetricSample[]
}) {
  const ratio =
    validUsage(used) && Number.isFinite(total) && total > 0 ? (used / total) * 100 : null
  const maximum = Math.max(
    1,
    Number.isFinite(total) ? total : 0,
    ...samples.map((sample) => sample[field]),
  )
  const start = samples.length ? Date.parse(samples[0].at) : 0
  const duration = samples.length
    ? Math.max(1, Date.parse(samples[samples.length - 1].at) - start)
    : 1
  const path = samples
    .map((sample, index) => {
      const x = 2 + ((Date.parse(sample.at) - start) / duration) * 236
      const y = 50 - (sample[field] / maximum) * 46
      return `${index ? 'L' : 'M'}${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
  return (
    <div className={`node-runtime-metric node-runtime-${field}`}>
      <div className="node-runtime-metric-heading">
        <span>{label}</span>
        {ratio !== null && ratio > 85 && <small className="node-runtime-high">High usage</small>}
      </div>
      <strong>{formatUsage(used, field)}</strong>
      <div className="node-runtime-meter-row">
        <div
          className="node-runtime-meter"
          role={ratio === null ? undefined : 'meter'}
          aria-hidden={ratio === null ? true : undefined}
          aria-label={ratio === null ? undefined : `${label} usage against allocatable capacity`}
          aria-valuemin={ratio === null ? undefined : 0}
          aria-valuemax={ratio === null ? undefined : 100}
          aria-valuenow={ratio === null ? undefined : Math.min(100, ratio)}
          aria-valuetext={
            ratio === null ? undefined : `${ratio.toFixed(1)} percent of allocatable capacity`
          }
        >
          <i style={{ width: ratio === null ? 0 : `${Math.min(100, ratio)}%` }} />
        </div>
        <span>{ratio === null ? '—' : `${ratio.toFixed(1)}%`}</span>
      </div>
      {samples.length < 2 ? (
        <p className="node-runtime-wait">Waiting for a second source sample.</p>
      ) : (
        <svg
          viewBox="0 0 240 54"
          preserveAspectRatio="none"
          className="node-runtime-chart"
          role="img"
          aria-label={`${label} history: ${samples.length} source samples`}
        >
          <title>
            {label} from {timestamp(samples[0].at)} to {timestamp(samples[samples.length - 1].at)}
          </title>
          <path className="node-runtime-baseline" d="M2,50 H238" />
          <path className="node-runtime-line" d={path} />
        </svg>
      )}
    </div>
  )
}
