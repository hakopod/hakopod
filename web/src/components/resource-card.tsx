import type { ReactNode } from 'react'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { APIError, relative, timestamp } from '../lib/api'
import { Icon } from './icons'
import { Button } from './ui/button'
import { ErrorState, Status } from './shared'

// Every card renders the same five slots, so siblings in one grid row share baselines.
export function ResourceCard({
  cue,
  name,
  menu,
  meta,
  endpoint,
  state,
  metric,
}: {
  cue: ReactNode
  name: ReactNode
  menu?: ReactNode
  meta: ReactNode
  endpoint?: ReactNode
  state: ReactNode
  metric?: ReactNode
}) {
  return (
    <article className="ops-catalog-card resource-card interactive">
      <Brackets />
      <div className="resource-card-top">
        <span className="resource-card-cue" aria-hidden="true">
          {cue}
        </span>
        <h2>{name}</h2>
        {menu && <div className="resource-card-menu">{menu}</div>}
      </div>
      <p className="resource-card-meta">{meta}</p>
      <div className="resource-card-endpoint">
        {endpoint ?? <span className="resource-card-absent">No endpoint</span>}
      </div>
      <div className="resource-card-state">{state}</div>
      <div className="resource-card-metric">{metric}</div>
    </article>
  )
}

export function ObservedState({
  status,
  observedAt,
  freshness = 'Not observed',
  note,
}: {
  status?: string
  observedAt?: string
  freshness?: string
  note?: string
}) {
  return (
    <>
      <div className="resource-state-line">
        <Status value={status} small />
        <span className="resource-freshness">
          {observedAt ? (
            <time dateTime={observedAt} title={timestamp(observedAt)}>
              Observed {relative(observedAt).toLowerCase()}
            </time>
          ) : (
            freshness
          )}
        </span>
      </div>
      {note && (
        <p className="resource-state-note" title={note}>
          {note}
        </p>
      )}
    </>
  )
}

// Unknown values read "Unavailable"; a missing number is never shown as zero.
export function CardMetric({
  label,
  window,
  value,
  unit,
}: {
  label: string
  window: string
  value?: number | string
  unit?: string
}) {
  const known = value !== undefined && value !== ''
  return (
    <dl className="resource-metric">
      <dt>
        {label} <span>· {window}</span>
      </dt>
      <dd className={known ? undefined : 'resource-card-absent'}>
        {known ? (
          <>
            {value}
            {unit && <small> {unit}</small>}
          </>
        ) : (
          'Unavailable'
        )}
      </dd>
    </dl>
  )
}

export function ExternalEndpoint({ url, label }: { url: string; label: string }) {
  return (
    <a
      className="resource-endpoint-link"
      href={url}
      target="_blank"
      rel="noreferrer"
      title={url}
      aria-label={`${label}: ${url} (opens in a new tab)`}
    >
      <span>{url.replace(/^https?:\/\//, '').replace(/\/$/, '')}</span>
      <Icon name="external" size={12} />
    </a>
  )
}

export function ResourceRows({
  label,
  className = '',
  children,
}: {
  label: string
  className?: string
  children: ReactNode
}) {
  return (
    <ul className={`resource-rows ${className}`} aria-label={label}>
      {children}
    </ul>
  )
}

// Identity grows, status fits, metadata keeps a stable column and actions stay aligned.
export function ResourceRow({
  identity,
  detail,
  status,
  meta,
  actions,
}: {
  identity: ReactNode
  detail?: ReactNode
  status?: ReactNode
  meta?: ReactNode
  actions?: ReactNode
}) {
  return (
    <li className="resource-row">
      <div className="resource-row-identity">
        <div className="resource-row-name">{identity}</div>
        {detail && <div className="resource-row-detail">{detail}</div>}
      </div>
      <div className="resource-row-status">{status}</div>
      <div className="resource-row-meta">{meta}</div>
      <div className="resource-row-actions">{actions}</div>
    </li>
  )
}

// What is absent and at most one next step, without a hero panel.
export function ResourceEmpty({
  icon = 'box',
  title,
  description,
  action,
}: {
  icon?: string
  title: string
  description?: string
  action?: ReactNode
}) {
  return (
    <div className="resource-empty" role="status">
      <Icon name={icon} size={18} />
      <p>
        <strong>{title}</strong>
        {description && <span>{description}</span>}
      </p>
      {action}
    </div>
  )
}

// First-run state: what this place is for, the first step, and what follows it.
export function GuidedEmpty({
  icon,
  satellites,
  title,
  description,
  primary,
  secondary,
  steps,
  tip,
}: {
  icon: string
  satellites: { icon: string; label: string }[]
  title: string
  description: string
  primary?: ReactNode
  secondary?: ReactNode
  steps?: { title: string; detail: string }[]
  tip?: ReactNode
}) {
  return (
    <section className="guided-empty" aria-label={title}>
      <div className="guided-empty-body">
        <div className="guided-empty-art" aria-hidden="true">
          <svg viewBox="0 0 240 112" className="guided-empty-links">
            {satellites.slice(0, 4).map((_, i) => (
              <path key={i} d={`M120 56 L${i % 2 ? 196 : 44} ${i < 2 ? 26 : 86}`} />
            ))}
          </svg>
          <span className="guided-empty-hub">
            <Icon name={icon} size={20} />
          </span>
          {satellites.slice(0, 4).map((satellite, i) => (
            <span key={i} className="guided-empty-satellite" data-slot={i}>
              <span className="guided-empty-tile">
                <Icon name={satellite.icon} size={14} />
              </span>
              <span className="guided-empty-label">{satellite.label}</span>
            </span>
          ))}
        </div>
        <h2>{title}</h2>
        <p>{description}</p>
        {(primary || secondary) && (
          <div className="guided-empty-actions">
            {primary}
            {secondary}
          </div>
        )}
        {tip && <p className="guided-empty-tip">{tip}</p>}
      </div>
      {steps && steps.length > 0 && (
        <ol className="guided-empty-steps">
          {steps.map((step, i) => (
            <li key={step.title}>
              <span>{String(i + 1).padStart(2, '0')}</span>
              <strong>{step.title}</strong>
              <p>{step.detail}</p>
            </li>
          ))}
        </ol>
      )}
    </section>
  )
}

// Permission failures are their own state; other errors keep the shared retry path.
export function ResourceError({
  error,
  retry,
  subject,
}: {
  error: unknown
  retry: () => void
  subject: string
}) {
  if (error instanceof APIError && error.status === 403)
    return (
      <ResourceEmpty
        icon="lock"
        title={`You do not have access to ${subject}`}
        description="Ask a project administrator for access."
      />
    )
  return <ErrorState error={error} retry={retry} />
}

// A failed refresh keeps the last data visible and says how old it is.
export function StaleNotice({ updatedAt, retry }: { updatedAt: number; retry: () => void }) {
  const at = new Date(updatedAt).toISOString()
  return (
    <div className="resource-stale" role="status">
      <Icon name="alert" size={14} />
      <span>
        Refresh failed. Showing data observed{' '}
        <time dateTime={at} title={timestamp(at)}>
          {relative(at).toLowerCase()}
        </time>
        .
      </span>
      <Button size="sm" variant="ghost" onClick={retry}>
        Retry
      </Button>
    </div>
  )
}
