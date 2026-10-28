import { useEffect, useState } from 'react'
import { Link, useLocation } from '@tanstack/react-router'
import { TriangleAlert } from 'lucide-react'
import { bannerCopy, useSecurityOverview, type SecurityOverview } from '../lib/security'
import { Button } from './ui/button'
import { Icon } from './icons'
import '../styles/security.css'

const storageKey = 'hakopod.advisory-banner.dismissed'

// Identifies the current set of findings: affected app ids plus the newest advisory revision.
export function findingsHash(overview: SecurityOverview) {
  const ids = overview.applications
    .filter((app) => app.findings.length > 0)
    .map((app) => app.application_id)
    .sort()
  const revision = Math.max(
    0,
    ...overview.applications.flatMap((app) => app.findings.map((f) => f.advisory_revision)),
  )
  const text = `${ids.join(',')}|${revision}`
  let hash = 5381
  for (let i = 0; i < text.length; i++) hash = ((hash * 33) ^ text.charCodeAt(i)) >>> 0
  return hash.toString(36)
}

export function SecurityBannerView({
  overview,
  onDismiss,
}: {
  overview: SecurityOverview | undefined
  onDismiss?: () => void
}) {
  const copy = bannerCopy(overview)
  if (!copy) return null
  const critical = overview!.summary.critical > 0
  return (
    <section
      className="advisory-banner"
      role="region"
      aria-label="Security update"
      data-critical={critical}
    >
      <TriangleAlert size={20} aria-hidden="true" />
      <div>
        <span className="advisory-eyebrow">{copy.eyebrow}</span>
        <strong>{copy.title}</strong>
        <p>{copy.body}</p>
      </div>
      <div className="advisory-banner-actions">
        <Button size="sm" variant="primary" asChild>
          <Link to="/security">
            Review update
            <Icon name="arrow" size={14} />
          </Link>
        </Button>
        {onDismiss && (
          <Button
            size="sm"
            variant="ghost"
            aria-label="Dismiss security update"
            onClick={onDismiss}
          >
            <Icon name="x" size={16} />
          </Button>
        )}
      </div>
    </section>
  )
}

export function SecurityBanner() {
  const path = useLocation().pathname
  const overview = useSecurityOverview()
  const [dismissed, setDismissed] = useState<string | null>(null)
  useEffect(() => {
    try {
      setDismissed(localStorage.getItem(storageKey))
    } catch {
      // Storage unavailable: the banner stays visible.
    }
  }, [])
  if (path === '/security' || !overview.data) return null
  const hash = findingsHash(overview.data)
  if (dismissed === hash) return null
  return (
    <SecurityBannerView
      overview={overview.data}
      onDismiss={() => {
        setDismissed(hash)
        document.getElementById('main-content')?.focus()
        try {
          localStorage.setItem(storageKey, hash)
        } catch {
          // Dismissal lasts for this page view only.
        }
      }}
    />
  )
}
