import { useEffect, useId, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ChevronRight } from 'lucide-react'
import { APIError, message, relative, timestamp } from '../lib/api'
import {
  applySecurityFix,
  checkApplicationSecurity,
  fixLine,
  fixPrompt,
  invalidateSecurity,
  putSecuritySettings,
  type ApplicationSecurity,
  type SecurityFinding,
  type SecurityOverview,
  type SecuritySettings,
} from '../lib/security'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Input } from './ui/input'
import { Badge } from './ui/surfaces'
import { Toast } from './ui/toast'
import { Copy, Note } from './shared'
import { ResourceEmpty } from './resource-card'
import '../styles/security.css'

export const disclaimer = 'No findings does not mean an app is free of vulnerabilities.'
const severities = ['critical', 'high', 'moderate', 'low'] as const
type Severity = (typeof severities)[number]
// Critical and high differ in colour as well as text; moderate and low stay quiet.
const tone = { critical: 'danger', high: 'warning', moderate: 'neutral', low: 'neutral' } as const
const plural = (count: number, one: string, many = `${one}s`) =>
  `${count} ${count === 1 ? one : many}`

export function feedState(feed: SecurityOverview['feed']) {
  if (!feed.feed_enabled && !feed.osv_enabled) return 'Disabled'
  return feed.stale || feed.error ? 'Stale' : 'Current'
}

// One advisory can match several services; the triage list shows it once with every service.
export type Advisory = { id: string; severity: Severity; findings: SecurityFinding[] }

export function groupAdvisories(findings: SecurityFinding[]): Advisory[] {
  const byId = new Map<string, Advisory>()
  for (const f of findings) {
    const entry = byId.get(f.advisory_id)
    if (entry) entry.findings.push(f)
    else byId.set(f.advisory_id, { id: f.advisory_id, severity: f.severity, findings: [f] })
  }
  return [...byId.values()].sort(
    (a, b) =>
      severities.indexOf(a.severity) - severities.indexOf(b.severity) ||
      a.findings[0].title.localeCompare(b.findings[0].title),
  )
}

function severityCounts(advisories: Advisory[]) {
  const counts: Record<Severity, number> = { critical: 0, high: 0, moderate: 0, low: 0 }
  for (const a of advisories) counts[a.severity]++
  return counts
}

// Most severe first: compare critical counts, then high, and so on; incomplete-only apps last.
export function triageOrder(apps: ApplicationSecurity[]) {
  const ranked = apps.map((app) => {
    const counts = severityCounts(groupAdvisories(app.findings))
    return { app, counts: severities.map((level) => counts[level]) }
  })
  return ranked
    .sort((a, b) => {
      for (let i = 0; i < severities.length; i++)
        if (a.counts[i] !== b.counts[i]) return b.counts[i] - a.counts[i]
      return a.app.application_name.localeCompare(b.app.application_name)
    })
    .map(({ app }) => app)
}

export function fixStatus(advisories: Advisory[]) {
  const manual = advisories.filter((a) => a.findings.every((f) => f.fix === 'manual')).length
  const fixable = advisories.length - manual
  if (!fixable) return 'Manual change needed'
  if (!manual) return fixLine(advisories.find((a) => a.findings[0].fix !== 'manual')!.findings[0])
  return `${plural(fixable, 'update')} available · ${manual} manual`
}

export function SecurityReview({
  overview,
  settings,
  expanded,
}: {
  overview: SecurityOverview
  settings?: SecuritySettings
  expanded?: string
}) {
  const listed = triageOrder(
    overview.applications.filter((app) => app.findings.length > 0 || app.incomplete.length > 0),
  )
  const [open, setOpen] = useState<Set<string>>(() => new Set(expanded ? [expanded] : []))
  const [editingSources, setEditingSources] = useState(false)
  useEffect(() => {
    if (expanded) document.getElementById(`advisory-${expanded}`)?.scrollIntoView()
  }, [expanded])
  const toggle = (id: string) =>
    setOpen((current) => {
      const next = new Set(current)
      if (!next.delete(id)) next.add(id)
      return next
    })
  return (
    <>
      <div className="security-summary">
        <Summary overview={overview} />
        <Sources
          feed={overview.feed}
          onEdit={settings?.can_manage ? () => setEditingSources(true) : undefined}
        />
      </div>
      {feedState(overview.feed) === 'Stale' && (
        <Note>
          {overview.feed.error
            ? `The advisory feed is stale (${overview.feed.error}). Updates from the Hakopod feed are paused until it is current.`
            : 'The advisory feed is stale.'}
        </Note>
      )}
      {listed.length === 0 ? (
        <ResourceEmpty icon="shield" title="No affected apps" description={disclaimer} />
      ) : (
        <ul className="security-rows" aria-label="Apps with advisories">
          {listed.map((app) => (
            <SecurityAppRow
              key={app.application_id}
              app={app}
              open={open.has(app.application_id)}
              onToggle={() => toggle(app.application_id)}
            />
          ))}
        </ul>
      )}
      {settings?.can_manage && editingSources && (
        <SourceSettings settings={settings} onClose={() => setEditingSources(false)} />
      )}
    </>
  )
}

function Summary({ overview }: { overview: SecurityOverview }) {
  const affected = overview.applications.filter((app) => app.findings.length > 0)
  const advisories = new Map<string, Severity>()
  for (const app of affected) for (const f of app.findings) advisories.set(f.advisory_id, f.severity)
  const critical = [...advisories.values()].filter((level) => level === 'critical').length
  const incomplete = overview.applications.filter((app) => app.incomplete.length > 0).length
  const clear = overview.applications.length - affected.length
  const parts = [
    affected.length ? plural(affected.length, 'app') + ' affected' : 'No affected apps',
    affected.length > 0 && plural(critical, 'critical advisory', 'critical advisories'),
    affected.length > 0 && `${plural(advisories.size, 'advisory', 'advisories')} in total`,
    incomplete > 0 && plural(incomplete, 'incomplete check'),
    clear > 0 && `${plural(clear, 'app')} with no findings`,
  ].filter(Boolean)
  return <p className="security-summary-line">{parts.join(' · ')}</p>
}

function Sources({ feed, onEdit }: { feed: SecurityOverview['feed']; onEdit?: () => void }) {
  const names = [feed.feed_enabled && 'Hakopod feed', feed.osv_enabled && 'OSV.dev'].filter(Boolean)
  const state = feedState(feed)
  return (
    <p className="security-sources">
      <span>Sources: {names.length ? names.join(', ') : 'none, checks are off'}</span>
      {state !== 'Disabled' && feed.checked_at && (
        <span>
          {state === 'Stale' ? 'Stale' : 'Current'}, checked{' '}
          <time dateTime={feed.checked_at} title={timestamp(feed.checked_at)}>
            {relative(feed.checked_at).toLowerCase()}
          </time>
        </span>
      )}
      {onEdit && (
        <Button type="button" variant="ghost" size="sm" onClick={onEdit}>
          Edit sources
        </Button>
      )}
    </p>
  )
}

function SecurityAppRow({
  app,
  open,
  onToggle,
}: {
  app: ApplicationSecurity
  open: boolean
  onToggle: () => void
}) {
  const panelId = useId()
  const queryClient = useQueryClient()
  const [checking, setChecking] = useState(false)
  const [checkError, setCheckError] = useState('')
  const [fixing, setFixing] = useState<SecurityFinding | null>(null)
  const advisories = groupAdvisories(app.findings)
  const counts = severityCounts(advisories)
  const fixable = app.can_write && app.findings.some((f) => f.fix !== 'manual')
  const fixes = new Set(advisories.map((a) => fixText(a.findings[0])))
  // When every advisory has the same fix, say it once instead of in every row.
  const sharedFix = advisories.length > 1 && fixes.size === 1 ? [...fixes][0] : ''

  const check = async () => {
    setChecking(true)
    setCheckError('')
    try {
      queryClient.setQueryData(
        ['security', 'application', app.application_id],
        await checkApplicationSecurity(app.application_id),
      )
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

  return (
    <li className="security-row" id={`advisory-${app.application_id}`} data-open={open}>
      <button
        type="button"
        className="security-row-toggle"
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        aria-label={`${open ? 'Hide' : 'Show'} advisories for ${app.application_name}`}
        onClick={onToggle}
      >
        <ChevronRight size={16} aria-hidden="true" />
      </button>
      <div className="security-row-identity">
        <span className="security-row-name">
          <Link to="/applications/$applicationId" params={{ applicationId: app.application_id }}>
            {app.application_name}
          </Link>
          {app.auto_patch && <Badge>Auto-patch on</Badge>}
          {app.incomplete.length > 0 && <Badge tone="warning">Check incomplete</Badge>}
        </span>
        <span className="security-row-scope">
          {app.project} · {app.environment}
        </span>
      </div>
      <div className="security-row-severity" role="group" aria-label="Advisories by severity">
        {advisories.length ? (
          severities
            .filter((level) => counts[level])
            .map((level) => (
              <Badge key={level} tone={tone[level]}>
                {counts[level]} {level}
              </Badge>
            ))
        ) : (
          <span className="security-muted">No advisories found</span>
        )}
      </div>
      <div className="security-row-fix">{advisories.length ? fixStatus(advisories) : ''}</div>
      <div className="security-row-checked">
        {app.checked_at ? (
          <time dateTime={app.checked_at} title={timestamp(app.checked_at)}>
            Checked {relative(app.checked_at).toLowerCase()}
          </time>
        ) : (
          'Not checked yet'
        )}
      </div>
      <div className="security-row-actions">
        {app.findings.length > 0 && <Copy value={fixPrompt(app)} label="Copy fix prompt" />}
        {app.can_write && (
          <Button type="button" variant="secondary" size="sm" disabled={checking} onClick={check}>
            {checking ? 'Checking…' : 'Check again'}
          </Button>
        )}
      </div>
      {open && (
        <div className="security-row-panel" id={panelId}>
          {checkError && (
            <p className="security-error" role="alert">
              {checkError}
            </p>
          )}
          {app.incomplete.length > 0 && (
            <Note>
              <strong>Check incomplete</strong>
              <ul>
                {app.incomplete.map((line) => (
                  <li key={line}>{line}</li>
                ))}
              </ul>
            </Note>
          )}
          {app.unknown_services.length > 0 && (
            <p className="security-muted">
              Runtime version not recorded for: {app.unknown_services.join(', ')}. Image advisories
              still apply.
            </p>
          )}
          {sharedFix && (
            <p className="security-shared-fix">
              <span className="security-muted">Fix for every advisory</span> {sharedFix}
            </p>
          )}
          {advisories.length > 0 && (
            <table className="security-advisories">
              <caption className="sr-only">Advisories for {app.application_name}</caption>
              <thead>
                <tr>
                  <th scope="col">Severity</th>
                  <th scope="col">Advisory</th>
                  <th scope="col">Package</th>
                  {!sharedFix && <th scope="col">Fix</th>}
                  <th scope="col">Services</th>
                  {fixable && (
                    <th scope="col">
                      <span className="sr-only">Actions</span>
                    </th>
                  )}
                </tr>
              </thead>
              <tbody>
                {advisories.map((advisory) => (
                  <AdvisoryRow
                    key={advisory.id}
                    advisory={advisory}
                    actions={fixable}
                    showFix={!sharedFix}
                    onFix={setFixing}
                  />
                ))}
              </tbody>
            </table>
          )}
          {advisories.length > 3 && (
            <div>
              <Button type="button" variant="ghost" size="sm" onClick={onToggle}>
                Hide advisories
              </Button>
            </div>
          )}
        </div>
      )}
      {fixing && <FixDialog app={app} finding={fixing} onClose={() => setFixing(null)} />}
    </li>
  )
}

const fixText = (f: SecurityFinding) => (f.fix === 'manual' ? f.fix_reason || fixLine(f) : fixLine(f))

function AdvisoryRow({
  advisory,
  actions,
  showFix,
  onFix,
}: {
  advisory: Advisory
  actions: boolean
  showFix: boolean
  onFix: (finding: SecurityFinding) => void
}) {
  const f = advisory.findings[0]
  const label = f.aliases?.find((alias) => alias.startsWith('CVE-')) || f.advisory_id
  const packages = [...new Set(advisory.findings.map((item) => item.evidence))]
  const services = advisory.findings.map((item) => item.service)
  const possible = advisory.findings.every((item) => item.confidence === 'possible')
  // The API omits fixed_digests when it is empty.
  const digest = f.fixed_digests?.[0]
  return (
    <tr>
      <td data-label="Severity">
        <Badge tone={tone[advisory.severity]}>
          {advisory.severity.charAt(0).toUpperCase() + advisory.severity.slice(1)}
        </Badge>
      </td>
      <td data-label="Advisory" className="security-advisory-title">
        <a href={f.url} target="_blank" rel="noreferrer">
          {f.title}
        </a>
        <span className="security-muted">
          {label} · {f.source === 'osv' ? 'OSV.dev' : 'Hakopod feed'}
          {possible && ' · Possibly affected'}
        </span>
      </td>
      <td data-label="Package">
        {packages.map((value) => (
          <code key={value}>{value}</code>
        ))}
      </td>
      {showFix && (
        <td data-label="Fix">
          {fixText(f)}
          {digest && (
            <span className="security-muted" title={digest}>
              Fixed digest {digest.replace(/^sha256:/, '').slice(0, 12)}
            </span>
          )}
        </td>
      )}
      <td data-label="Services">{services.join(', ')}</td>
      {actions && (
        <td className="security-advisory-actions">
          {advisory.findings
            .filter((item) => item.fix !== 'manual')
            .map((item) => (
              <Button
                key={item.service}
                type="button"
                size="sm"
                variant="secondary"
                onClick={() => onFix(item)}
              >
                {item.fix === 'rebuild' ? 'Rebuild' : 'Update'} {item.service}
              </Button>
            ))}
        </td>
      )}
    </tr>
  )
}

function FixDialog({
  app,
  finding,
  onClose,
}: {
  app: ApplicationSecurity
  finding: SecurityFinding
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState('')
  const rebuild = finding.fix === 'rebuild'
  const verb = rebuild ? 'Rebuild' : 'Update'
  const apply = async () => {
    setBusy(true)
    setError('')
    try {
      await applySecurityFix(
        app.application_id,
        { advisory_id: finding.advisory_id, service: finding.service, expected_revision: app.revision },
        crypto.randomUUID(),
      )
      setDone(rebuild ? 'Rebuild started.' : 'Update started.')
      await invalidateSecurity(queryClient)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  if (done) return <Toast open onOpenChange={(value) => !value && onClose()} message={done} />
  return (
    <Dialog
      open
      className="security-dialog"
      onOpenChange={(value) => !value && !busy && onClose()}
      title={`${verb} ${finding.service} in ${app.application_name}?`}
      description={
        rebuild
          ? 'Rebuilds the current commit and deploys the new image.'
          : `Creates revision ${app.revision + 1} with image tags resolved again. Running pods are replaced by a normal rollout.`
      }
    >
      <div className="dialog-body grid min-w-0 gap-3">
        <p className="text-sm font-medium [overflow-wrap:anywhere]">{finding.title}</p>
        {error && (
          <p className="security-error" role="alert">
            {error}
          </p>
        )}
      </div>
      <div className="dialog-footer flex-wrap">
        <Button type="button" variant="secondary" disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        <Button type="button" variant="primary" disabled={busy} onClick={() => void apply()}>
          {busy ? `${verb === 'Rebuild' ? 'Rebuilding' : 'Updating'}…` : `${verb} ${finding.service}`}
        </Button>
      </div>
    </Dialog>
  )
}

function SourceSettings({
  settings,
  onClose,
}: {
  settings: SecuritySettings
  onClose: () => void
}) {
  const cache = useQueryClient()
  const [draft, setDraft] = useState({
    feed_enabled: settings.feed_enabled,
    osv_enabled: settings.osv_enabled,
  })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const save = async () => {
    setBusy(true)
    setError('')
    try {
      await putSecuritySettings({ ...draft, expected_revision: settings.revision })
      await invalidateSecurity(cache)
      onClose()
    } catch (err) {
      setError(message(err))
      if (err instanceof APIError && err.status === 409)
        void cache.invalidateQueries({ queryKey: ['security', 'settings'] })
    } finally {
      setBusy(false)
    }
  }
  const sources = [
    ['feed_enabled', 'Hakopod feed', 'Signed advisories published by Hakopod.'],
    ['osv_enabled', 'OSV.dev', 'Open Source Vulnerabilities database.'],
  ] as const
  return (
    <Dialog
      open
      className="security-dialog"
      onOpenChange={(value) => !value && !busy && onClose()}
      title="Advisory sources"
      description="Choose where Hakopod looks up advisories for running apps."
    >
      <div className="dialog-body security-source-options">
        {sources.map(([key, name, help]) => (
          <label key={key}>
            <Input
              type="checkbox"
              checked={draft[key]}
              disabled={busy}
              onChange={(event) => setDraft({ ...draft, [key]: event.target.checked })}
            />
            <span>
              {name}
              <small>{help}</small>
            </span>
          </label>
        ))}
        {!draft.feed_enabled && !draft.osv_enabled && (
          <Note>With both sources off, Hakopod stops checking apps for advisories.</Note>
        )}
        {error && (
          <p className="security-error" role="alert">
            {error}
          </p>
        )}
      </div>
      <div className="dialog-footer flex-wrap">
        <Button type="button" variant="secondary" disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        <Button type="button" variant="primary" disabled={busy} onClick={() => void save()}>
          {busy ? 'Saving sources…' : 'Save sources'}
        </Button>
      </div>
    </Dialog>
  )
}
