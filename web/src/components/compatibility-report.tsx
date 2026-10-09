import { Status } from './shared'

export type CompatibilityCheck = {
  code: string
  status: 'checked' | 'unknown' | 'warning' | 'blocker'
  message: string
  evidence?: string
}
export type CompatibilityReportValue = {
  generated_at: string
  checks: CompatibilityCheck[]
  blocked: boolean
}

export function CompatibilityReport({ value }: { value?: CompatibilityReportValue }) {
  if (!value) return null
  const needsReview = value.checks.some(
    (check) => check.status === 'unknown' || check.status === 'warning',
  )
  return (
    <section aria-label="Restore compatibility report" className="grid gap-3">
      <div className="flex items-center justify-between gap-3">
        <h3 className="font-medium">Restore compatibility</h3>
        <Status value={value.blocked ? 'blocked' : needsReview ? 'review required' : 'ready'} />
      </div>
      <dl className="grid gap-4">
        {value.checks.map((check) => (
          <div
            key={check.code}
            className="grid min-w-0 gap-1 sm:grid-cols-[145px_minmax(0,1fr)] sm:gap-4"
          >
            <dt className="flex min-w-0 flex-wrap items-center gap-2 text-xs text-muted">
              <Status value={check.status === 'checked' ? 'ready' : check.status} />
              <span>{check.code.replaceAll('_', ' ')}</span>
            </dt>
            <dd className="m-0 min-w-0 text-xs">
              {check.message}
              {check.evidence && (
                <>
                  <br />
                  <code className="break-all">{check.evidence}</code>
                </>
              )}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  )
}
