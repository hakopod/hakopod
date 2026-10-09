import { Status } from './shared'

export type CompatibilityCheck = { code: string; status: 'checked' | 'unknown' | 'blocker'; message: string; evidence?: string }
export type CompatibilityReportValue = { generated_at: string; checks: CompatibilityCheck[]; blocked: boolean }

export function CompatibilityReport({ value }: { value?: CompatibilityReportValue }) {
  if (!value) return null
  return <section aria-label="Restore compatibility report" className="grid gap-3">
    <div className="flex items-center justify-between gap-3"><h3 className="font-medium">Restore compatibility</h3><Status value={value.blocked ? 'blocked' : 'ready'} /></div>
    <dl className="service-definition-list">
      {value.checks.map((check) => <div key={check.code}><dt className="flex items-center gap-2"><Status value={check.status === 'checked' ? 'ready' : check.status} />{check.code.replaceAll('_', ' ')}</dt><dd>{check.message}{check.evidence && <><br/><code className="break-all">{check.evidence}</code></>}</dd></div>)}
    </dl>
  </section>
}
