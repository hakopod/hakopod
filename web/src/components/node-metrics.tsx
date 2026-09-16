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
import { Note, RequestError } from './shared'
import { ResourceMetric, validMetricUsage } from './resource-metric'

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
    metrics.available &&
    validMetricUsage(metrics.cpu_millicores) &&
    validMetricUsage(metrics.memory_bytes)
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
      {error ? <RequestError error={message(error)} /> : null}
      <div className="node-runtime-values">
        <ResourceMetric
          label="CPU"
          field="cpu"
          used={available ? metrics.cpu_millicores : undefined}
          total={cpuCapacity}
          samples={samples}
        />
        <ResourceMetric
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
