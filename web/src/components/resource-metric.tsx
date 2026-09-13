import type { MetricSample } from '../lib/runtime-metrics'

const sampleTime = new Intl.DateTimeFormat(undefined, {
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
})

export function validMetricUsage(value?: number): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}

function formatUsage(value: number | undefined, field: 'cpu' | 'memory') {
  if (!validMetricUsage(value)) return 'Unavailable'
  if (field === 'cpu')
    return `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })} mCPU`
  return value >= 1024 ** 3
    ? `${(value / 1024 ** 3).toFixed(2)} GiB`
    : `${(value / 1024 ** 2).toFixed(1)} MiB`
}

export function ResourceMetric({
  label,
  field,
  used,
  total,
  samples,
}: {
  label: string
  field: 'cpu' | 'memory'
  used?: number
  total?: number
  samples: MetricSample[]
}) {
  const ratio =
    validMetricUsage(used) && validMetricUsage(total) && total > 0 ? (used / total) * 100 : null
  const maximum = Math.max(
    1,
    validMetricUsage(total) ? total : 0,
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
      {total !== undefined && (
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
      )}
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
            {label} from {sampleTime.format(Date.parse(samples[0].at))} to{' '}
            {sampleTime.format(Date.parse(samples[samples.length - 1].at))}
          </title>
          <path className="node-runtime-baseline" d="M2,50 H238" />
          <path className="node-runtime-line" d={path} />
        </svg>
      )}
    </div>
  )
}
