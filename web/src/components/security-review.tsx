import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Bell } from 'lucide-react'
import { APIError, message, relative } from '../lib/api'
import {
  findingBadge,
  fixLine,
  invalidateSecurity,
  putSecuritySettings,
  type ApplicationSecurity,
  type SecurityFinding,
  type SecurityOverview,
  type SecuritySettings,
} from '../lib/security'
import { ApplicationSecurityView } from './application-security'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Badge } from './ui/surfaces'
import { Empty, Note } from './shared'
import { Icon } from './icons'
import '../styles/security.css'

export const disclaimer = 'No findings does not mean an app is free of vulnerabilities.'
const severityOrder = ['critical', 'high', 'moderate', 'low']

function mostSevere(findings: SecurityFinding[]): SecurityFinding | undefined {
  return [...findings].sort(
    (a, b) => severityOrder.indexOf(a.severity) - severityOrder.indexOf(b.severity),
  )[0]
}

export function feedState(feed: SecurityOverview['feed']) {
  if (!feed.feed_enabled && !feed.osv_enabled) return 'Disabled'
  return feed.stale || feed.error ? 'Stale' : 'Current'
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
  const affected = overview.applications.filter((app) => app.findings.length > 0)
  const incompleteOnly = overview.applications.filter(
    (app) => app.findings.length === 0 && app.incomplete.length > 0,
  )
  const incomplete = overview.applications.filter((app) => app.incomplete.length > 0).length
  const feed = overview.feed
  const state = feedState(feed)
  useEffect(() => {
    if (expanded) document.getElementById(`advisory-${expanded}`)?.scrollIntoView()
  }, [expanded])
  return (
    <>
      <dl className="advisory-summary">
        <div>
          <dt>Affected apps</dt>
          <dd>{overview.summary.affected_applications}</dd>
        </div>
        <div>
          <dt>Critical</dt>
          <dd>{overview.summary.critical}</dd>
        </div>
        <div>
          <dt>Incomplete checks</dt>
          <dd>{incomplete}</dd>
        </div>
        <div>
          <dt>Feed</dt>
          <dd>
            {state}
            {state !== 'Disabled' && (
              <span className="advisory-muted">
                {' '}
                · checked {relative(feed.checked_at).toLowerCase()}
              </span>
            )}
          </dd>
        </div>
      </dl>
      {state === 'Stale' && (
        <Note>
          {feed.error
            ? `The advisory feed is stale (${feed.error}). Updates from the Hakopod feed are paused until it is current.`
            : 'The advisory feed is stale.'}
        </Note>
      )}
      {affected.length === 0 ? (
        <Empty icon="shield" title="No affected apps" description={disclaimer} />
      ) : (
        <>
          <h2 className="advisory-section-title">
            Affected apps <Badge>{affected.length}</Badge>
          </h2>
          <div className="advisory-grid">
            {affected.map((app) => (
              <AppCard key={app.application_id} app={app} />
            ))}
          </div>
          {affected.map((app) => (
            <details
              key={app.application_id}
              id={`advisory-${app.application_id}`}
              className="advisory-detail"
              open={expanded === app.application_id}
            >
              <summary>{app.application_name}</summary>
              <div className="advisory-detail-body">
                <div>
                  <Button size="sm" asChild>
                    <Link
                      to="/applications/$applicationId"
                      params={{ applicationId: app.application_id }}
                    >
                      Open app
                    </Link>
                  </Button>
                </div>
                <ApplicationSecurityView security={app} embedded />
              </div>
            </details>
          ))}
        </>
      )}
      {incompleteOnly.length > 0 && (
        <>
          <h2 className="advisory-section-title">
            Incomplete checks <Badge>{incompleteOnly.length}</Badge>
          </h2>
          <div className="advisory-grid">
            {incompleteOnly.map((app) => (
              <div className="advisory-card advisory-card-plain" key={app.application_id}>
                <span className="advisory-app-name">
                  <strong>{app.application_name}</strong>
                  <Badge tone="warning">Check incomplete</Badge>
                </span>
                <small>
                  {app.project} · {app.environment}
                </small>
                <Note>
                  {app.incomplete.map((line) => (
                    <p key={line}>{line}</p>
                  ))}
                </Note>
              </div>
            ))}
          </div>
        </>
      )}
      <div className="advisory-footer">
        <span>
          <Bell size={14} aria-hidden="true" />
          Security advisories go to app notification channels that include them.
        </span>
        <span>Automatic patching is off by default.</span>
      </div>
      {settings?.can_manage && <SourceSettings settings={settings} />}
    </>
  )
}

function AppCard({ app }: { app: ApplicationSecurity }) {
  const top = mostSevere(app.findings)
  const confirmed = app.findings.some((f) => f.confidence === 'confirmed')
  return (
    <Link className="advisory-card" to="/security" search={{ application: app.application_id }}>
      <span className="advisory-card-icon">
        <Icon name="box" size={18} />
      </span>
      <span className="advisory-app-name">
        <strong>{app.application_name}</strong>
        <Badge tone={confirmed ? 'danger' : 'warning'}>
          {findingBadge({ confidence: confirmed ? 'confirmed' : 'possible' })}
        </Badge>
        {app.incomplete.length > 0 && <Badge tone="warning">Check incomplete</Badge>}
      </span>
      <Icon name="arrow" size={16} />
      <small>
        {app.project} · {app.environment}
      </small>
      <span>
        {top && <>{fixLine(top)} · </>}
        <span className="advisory-muted">
          {app.findings.length} {app.findings.length === 1 ? 'advisory' : 'advisories'}
        </span>
      </span>
    </Link>
  )
}

function SourceSettings({ settings }: { settings: SecuritySettings }) {
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
    } catch (err) {
      setError(message(err))
      if (err instanceof APIError && err.status === 409)
        void cache.invalidateQueries({ queryKey: ['security', 'settings'] })
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="advisory-settings" aria-label="Advisory sources">
      <h2 className="advisory-section-title">Advisory sources</h2>
      <label>
        <Input
          type="checkbox"
          checked={draft.feed_enabled}
          disabled={busy}
          onChange={(event) => setDraft({ ...draft, feed_enabled: event.target.checked })}
        />
        Hakopod feed
      </label>
      <label>
        <Input
          type="checkbox"
          checked={draft.osv_enabled}
          disabled={busy}
          onChange={(event) => setDraft({ ...draft, osv_enabled: event.target.checked })}
        />
        OSV.dev
      </label>
      {error && (
        <p className="advisory-error" role="alert">
          {error}
        </p>
      )}
      <div>
        <Button variant="primary" size="sm" disabled={busy} onClick={() => void save()}>
          Save
        </Button>
      </div>
    </section>
  )
}
