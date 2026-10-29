import { useId, useState } from 'react'
import { ChevronRight } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { APIError, message, relative, timestamp } from '../lib/api'
import {
  applySecurityFix,
  checkApplicationSecurity,
  findingBadge,
  fixLine,
  fixPrompt,
  invalidateSecurity,
  putSecurityPolicy,
  useApplicationSecurity,
  type ApplicationSecurity as Security,
  type SecurityFinding,
} from '../lib/security'
import { Copy, ErrorState, HeadingHelp, Loading, Note } from './shared'
import { Badge } from './ui/surfaces'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Toast } from './ui/toast'

export function ApplicationSecurity({ applicationId }: { applicationId: string }) {
  const query = useApplicationSecurity(applicationId)
  if (query.error instanceof APIError && query.error.status === 404) return null
  if (query.error) return <ErrorState error={query.error} retry={() => query.refetch()} />
  if (!query.data) return <Loading rows={1} />
  return <ApplicationSecurityView security={query.data} />
}

const capitalize = (value: string) => value.charAt(0).toUpperCase() + value.slice(1)
const tone = { critical: 'danger', high: 'danger', moderate: 'warning', low: 'neutral' } as const
const severities = ['critical', 'high', 'moderate', 'low'] as const

export function ApplicationSecurityView({
  security,
  embedded = false,
}: {
  security: Security
  embedded?: boolean
}) {
  const headingId = useId()
  const queryClient = useQueryClient()
  const id = security.application_id
  const [checking, setChecking] = useState(false)
  const [checkError, setCheckError] = useState('')
  const [fixing, setFixing] = useState<SecurityFinding | null>(null)
  const [policy, setPolicy] = useState<boolean | null>(null)
  const [busy, setBusy] = useState(false)
  const [dialogError, setDialogError] = useState('')
  const [toast, setToast] = useState('')
  const incomplete = security.incomplete
  const unknown = security.unknown_services
  const counts: Partial<Record<SecurityFinding['severity'], number>> = {}
  for (const f of security.findings) counts[f.severity] = (counts[f.severity] ?? 0) + 1
  const showPolicy = security.auto_patch || security.findings.some((f) => f.auto_eligible)

  const check = async () => {
    setChecking(true)
    setCheckError('')
    try {
      queryClient.setQueryData(['security', 'application', id], await checkApplicationSecurity(id))
      await invalidateSecurity(queryClient)
    } catch (error) {
      setCheckError(
        error instanceof APIError && error.status === 429
          ? 'This app was checked less than 30 seconds ago. Try again shortly.'
          : message(error),
      )
    } finally {
      setChecking(false)
    }
  }

  const close = () => {
    setFixing(null)
    setPolicy(null)
    setDialogError('')
  }

  const run = async (action: () => Promise<unknown>, done: string) => {
    setBusy(true)
    setDialogError('')
    try {
      await action()
      close()
      setToast(done)
      await invalidateSecurity(queryClient)
    } catch (error) {
      setDialogError(message(error))
    } finally {
      setBusy(false)
    }
  }

  const rebuild = fixing?.fix === 'rebuild'

  return (
    <section
      id={embedded ? undefined : 'security'}
      className="flex flex-col gap-2"
      aria-labelledby={embedded ? undefined : headingId}
      aria-label={embedded ? `Security for ${security.application_name}` : undefined}
    >
      {(!embedded || security.can_write) && (
        <div className="section-toolbar">
          {!embedded && (
            <div className="hako-section-heading-title">
              <h2 id={headingId}>Security</h2>
              <HeadingHelp title="Security">
                Hakopod compares the runtime and image versions this app runs against the Hakopod
                advisory feed and OSV.dev. No findings does not mean an app is free of
                vulnerabilities.
              </HeadingHelp>
            </div>
          )}
          {security.findings.length > 0 && (
            <Copy value={fixPrompt(security)} label="Copy fix prompt" className="ml-auto" />
          )}
          {security.can_write && (
            <Button type="button" variant="secondary" size="sm" disabled={checking} onClick={check}>
              {checking ? 'Checking…' : 'Check again'}
            </Button>
          )}
        </div>
      )}
      {checkError && (
        <p className="text-sm text-destructive" role="alert">
          {checkError}
        </p>
      )}
      {security.findings.length === 0 && incomplete.length === 0 && (
        <p className="text-sm text-muted-foreground">
          No known advisories affect this app.
          {security.checked_at && ` Checked ${relative(security.checked_at)}.`}
        </p>
      )}
      {security.findings.length > 0 && (
        <details className="group min-w-0 border-y border-border">
          <summary className="flex min-w-0 cursor-pointer list-none flex-wrap items-center gap-x-3 gap-y-1 py-3 [&::-webkit-details-marker]:hidden">
            <ChevronRight
              size={16}
              aria-hidden="true"
              className="shrink-0 transition-transform group-open:rotate-90"
            />
            <span className="font-medium">
              {security.findings.length}{' '}
              {security.findings.length === 1 ? 'advisory affects' : 'advisories affect'} this app
            </span>
            {severities
              .filter((level) => counts[level])
              .map((level) => (
                <Badge key={level} tone={tone[level]}>
                  {counts[level]} {level}
                </Badge>
              ))}
          </summary>
          <ul className="flex flex-col pb-1">
            {security.findings.map((f) => (
              <li
                key={`${f.advisory_id}:${f.service}`}
                className="flex min-w-0 items-start gap-3 border-t border-border py-3"
              >
                <span className="w-20 shrink-0">
                  <Badge tone={tone[f.severity]}>{capitalize(f.severity)}</Badge>
                </span>
                <div className="grid min-w-0 flex-1 gap-1">
                  <a
                    href={f.url}
                    target="_blank"
                    rel="noreferrer"
                    title={f.title}
                    className="min-w-0 truncate text-sm font-medium"
                  >
                    {f.title}
                  </a>
                  <p className="min-w-0 text-xs text-muted-foreground [overflow-wrap:anywhere]">
                    {findingBadge(f)} · {f.service}: {f.evidence} ·{' '}
                    {f.source === 'osv' ? 'OSV.dev' : 'Hakopod'}
                    {f.aliases && f.aliases.length > 0 && ` · ${f.aliases.slice(0, 2).join(', ')}`}
                  </p>
                  <p className="min-w-0 text-xs [overflow-wrap:anywhere]">
                    {f.fix === 'manual' ? f.fix_reason || fixLine(f) : fixLine(f)}
                  </p>
                </div>
                {security.can_write && f.fix !== 'manual' && (
                  <Button type="button" size="sm" variant="secondary" onClick={() => setFixing(f)}>
                    {f.fix === 'rebuild' ? 'Rebuild' : 'Update'}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </details>
      )}
      {unknown.length > 0 && (
        <p className="min-w-0 text-sm text-muted-foreground [overflow-wrap:anywhere]">
          Runtime version not recorded for: {unknown.join(', ')}. Image advisories still apply.
        </p>
      )}
      {incomplete.length > 0 && (
        <Note>
          <strong>Check incomplete</strong>
          <ul>
            {incomplete.map((entry) => (
              <li key={entry} className="[overflow-wrap:anywhere]">
                {entry}
              </li>
            ))}
          </ul>
        </Note>
      )}
      {security.can_write && showPolicy && (
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={security.auto_patch}
            onChange={(e) => setPolicy(e.target.checked)}
          />
          Automatic patching
          {security.auto_patch && security.approval_expires_at && (
            <span className="text-muted-foreground">
              Approval expires {timestamp(security.approval_expires_at)}
            </span>
          )}
        </label>
      )}
      <Dialog
        open={fixing !== null}
        onOpenChange={(open) => !open && !busy && close()}
        title={`${rebuild ? 'Rebuild' : 'Update'} ${security.application_name}?`}
        description={
          rebuild
            ? 'Rebuilds the current commit and deploys the new image.'
            : `Creates revision ${security.revision + 1} with image tags resolved again. Running pods are replaced by a normal rollout.`
        }
      >
        <div className="dialog-body grid min-w-0 gap-3">
          <p className="text-sm font-medium [overflow-wrap:anywhere]">{fixing?.title}</p>
          {dialogError && (
            <p className="text-sm text-destructive" role="alert">
              {dialogError}
            </p>
          )}
        </div>
        <div className="dialog-footer flex-wrap">
          <Button type="button" variant="secondary" disabled={busy} onClick={close}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="primary"
            disabled={busy}
            onClick={() =>
              fixing &&
              run(
                () =>
                  applySecurityFix(
                    id,
                    {
                      advisory_id: fixing.advisory_id,
                      service: fixing.service,
                      expected_revision: security.revision,
                    },
                    crypto.randomUUID(),
                  ),
                rebuild ? 'Rebuild started.' : 'Update started.',
              )
            }
          >
            {rebuild ? 'Rebuild' : 'Update'}
          </Button>
        </div>
      </Dialog>
      <Dialog
        open={policy !== null}
        onOpenChange={(open) => !open && !busy && close()}
        title={policy ? 'Turn on automatic patching' : 'Turn off automatic patching'}
        description={
          policy
            ? 'Hakopod deploys a fix by itself only for confirmed image advisories from the Hakopod feed, when the image tag can move and a fixed digest is published. Everything else still needs your review.'
            : 'Hakopod stops applying fixes for this app by itself. Findings and notifications continue.'
        }
      >
        <div className="dialog-body grid min-w-0 gap-3">
          {dialogError && (
            <p className="text-sm text-destructive" role="alert">
              {dialogError}
            </p>
          )}
        </div>
        <div className="dialog-footer flex-wrap">
          <Button type="button" variant="secondary" disabled={busy} onClick={close}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="primary"
            disabled={busy}
            onClick={() =>
              run(
                () =>
                  putSecurityPolicy(id, {
                    auto_patch: Boolean(policy),
                    expected_policy_revision: security.policy_revision,
                  }),
                policy ? 'Automatic patching is on.' : 'Automatic patching is off.',
              )
            }
          >
            {policy ? 'Turn on' : 'Turn off'}
          </Button>
        </div>
      </Dialog>
      {toast && <Toast open onOpenChange={(open) => !open && setToast('')} message={toast} />}
    </section>
  )
}
