import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Dialog } from './ui/dialog'
import { Icon } from './icons'
import { Status } from './shared'
import { client, unwrap } from '../lib/client'

const destinations = [
  ['/', 'grid', 'Applications'],
  ['/infrastructure', 'server', 'Infrastructure'],
  ['/builds', 'branch', 'Source builds'],
  ['/templates', 'box', 'Templates'],
  ['/settings', 'shield', 'Account and access'],
] as const
export default function CommandPalette({
  open,
  onOpenChange,
  project,
  environment,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  project: string
  environment: string
}) {
  const [search, setSearch] = useState('')
  const apps = useQuery({
    queryKey: ['command-applications', project, environment],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications', {
          signal,
          params: { query: { project, environment, limit: 50 } },
        }),
      ),
    enabled: open && Boolean(project && environment),
    gcTime: 0,
  })
  const query = search.toLowerCase()
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Jump to your workspace"
      description="Search navigation and the first 50 applications in this environment."
    >
      <div className="dialog-body command-body">
        <div className="search-input">
          <Icon name="search" />
          <input
            autoFocus
            aria-label="Search workspace"
            placeholder="Application or destination…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </div>
        <div className="command-results">
          <span className="eyebrow">NAVIGATE</span>
          {destinations
            .filter(([, , label]) => label.toLowerCase().includes(query))
            .map(([to, icon, label]) => (
              <Link key={to} to={to} className="command-result" onClick={() => onOpenChange(false)}>
                <Icon name={icon} />
                <span>{label}</span>
                <Icon name="arrow" size={14} />
              </Link>
            ))}
          {apps.data && (
            <span className="eyebrow">
              APPLICATIONS · {project} / {environment}
            </span>
          )}
          {apps.data?.items
            .filter((app) => app.name.toLowerCase().includes(query))
            .map((app) => (
              <Link
                to="/applications/$applicationId"
                params={{ applicationId: app.id }}
                key={app.id}
                className="command-result"
                onClick={() => onOpenChange(false)}
              >
                <Icon name="box" />
                <span>{app.name}</span>
                <Status value={app.observed?.status || 'not observed'} small />
              </Link>
            ))}
          {apps.error && (
            <p className="muted-text">
              Application search is unavailable. Navigation remains available.
            </p>
          )}
        </div>
      </div>
    </Dialog>
  )
}
