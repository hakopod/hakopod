import { useEditionFeatures } from '../lib/dashboard-edition'
import { dashboardEdition } from '../lib/dashboard-edition'
import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { Input } from '@hakopod/hatch-ui/components/input'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { applicationRuntimeHealth } from '../lib/runtime-health'
import { Dialog } from './ui/dialog'
import { Icon } from './icons'
import { Loading, Status } from './shared'

type Destination = {
  id: string
  label: string
  icon: string
  group: string
  action: () => void
  detail?: ReactNode
}

export default function CommandPalette({
  open,
  onOpenChange,
  project,
  environment,
  onPreferences,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  project: string
  environment: string
  onPreferences?: () => void
}) {
  const features = useEditionFeatures()
  const [search, setSearch] = useState('')
  const [active, setActive] = useState(0)
  const { identity } = useScope()
  const navigate = useNavigate()
  const resultId = useId()
  const resultList = useRef<HTMLDivElement>(null)
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
  const destinations: Destination[] = [
    {
      id: '/',
      label: 'All projects',
      icon: 'grid',
      group: 'Navigate',
      action: () => void navigate({ to: '/' }),
    },
    ...(project
      ? [
          {
            id: 'project-applications',
            label: 'Applications',
            icon: 'box',
            group: 'Navigate',
            action: () =>
              void navigate({
                to: '/projects/$project',
                params: { project },
                search: { environment: environment || undefined },
              }),
          },
        ]
      : []),
    ...[
      { to: '/templates', icon: 'box', label: 'Catalog' },
      { to: '/builds', icon: 'branch', label: 'Builds' },
      { to: '/networks', icon: 'network', label: 'Networks' },
      { to: '/infrastructure', icon: 'server', label: 'Infrastructure' },
      ...(identity.admin ? [{ to: '/backups', icon: 'archive', label: 'Backups' }] : []),
      { to: '/settings', icon: 'settings', label: 'Settings' },
      { to: '/alarms', icon: 'alert', label: 'Alarms' },
    ]
      .filter(({ to }) => dashboardEdition.navigation(to) && (features.git || to !== '/builds'))
      .map(({ to, icon, label }) => ({
        id: to,
        label,
        icon,
        group: 'Navigate',
        action: () => void navigate({ to }),
      })),
    ...(onPreferences
      ? [
          {
            id: 'preferences',
            label: 'Personal preferences',
            icon: 'user',
            group: 'Navigate',
            action: onPreferences,
          },
        ]
      : []),
    ...(apps.data?.items || []).map((app) => ({
      id: app.id,
      label: app.name,
      icon: 'box',
      group: 'Applications',
      action: () =>
        void navigate({ to: '/applications/$applicationId', params: { applicationId: app.id } }),
      detail: <Status value={applicationRuntimeHealth(app).status} small />,
    })),
  ]
  const query = search.trim().toLowerCase()
  const results = destinations.filter((item) => item.label.toLowerCase().includes(query))
  const selected = Math.min(active, Math.max(0, results.length - 1))
  useEffect(() => {
    resultList.current
      ?.querySelector('[aria-selected="true"]')
      ?.scrollIntoView({ block: 'nearest' })
  }, [selected, search])
  const run = (index: number) => {
    const result = results[index]
    if (!result) return
    onOpenChange(false)
    result.action()
  }
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Quick navigation"
      description="Find a page or one of the first 50 applications in this environment."
    >
      <div className="dialog-body hako-command-body">
        <Input
          className="hako-command-search"
          containerClassName="hako-command-input"
          aria-label="Search workspace"
          placeholder="Application or destination…"
          value={search}
          maxLength={256}
          onChange={(event) => {
            setSearch(event.target.value)
            setActive(0)
          }}
          role="combobox"
          aria-expanded={open}
          aria-autocomplete="list"
          aria-controls={resultId}
          aria-activedescendant={results.length ? `${resultId}-${selected}` : undefined}
          onKeyDown={(event) => {
            if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
              event.preventDefault()
              setActive(
                Math.max(
                  0,
                  Math.min(results.length - 1, selected + (event.key === 'ArrowDown' ? 1 : -1)),
                ),
              )
            } else if (event.key === 'Enter') {
              event.preventDefault()
              run(selected)
            } else if (event.key === 'Home') {
              event.preventDefault()
              setActive(0)
            } else if (event.key === 'End') {
              event.preventDefault()
              setActive(Math.max(0, results.length - 1))
            }
          }}
        />
        <div
          className="hako-command-results"
          id={resultId}
          role="listbox"
          aria-label="Workspace search results"
          ref={resultList}
        >
          {['Navigate', 'Applications'].map(
            (group) =>
              results.some((item) => item.group === group) && (
                <div key={group} role="group" aria-label={group}>
                  <div className="hako-command-group" aria-hidden="true">
                    {group}
                    {group === 'Applications' && (
                      <span>
                        {project} / {environment}
                      </span>
                    )}
                  </div>
                  {results.map(
                    (item, index) =>
                      item.group === group && (
                        <button
                          type="button"
                          id={`${resultId}-${index}`}
                          key={item.id}
                          className="hako-command-result interactive"
                          role="option"
                          aria-selected={index === selected}
                          tabIndex={-1}
                          onMouseEnter={() => setActive(index)}
                          onClick={() => run(index)}
                        >
                          <Icon name={item.icon} />
                          <span>{item.label}</span>
                          {item.detail || <Icon name="arrow" size={14} />}
                          <Brackets />
                        </button>
                      ),
                  )}
                </div>
              ),
          )}
        </div>
        {!results.length && (
          <p className="hako-command-empty" role="status">
            No matching applications or destinations.
          </p>
        )}
        {apps.isFetching && <Loading rows={2} />}
        {apps.error && (
          <p className="hako-command-notice" role="status">
            Application search is unavailable. You can still open a page from navigation.
          </p>
        )}
        <p className="hako-command-help">
          <span>↑ ↓ to navigate</span>
          <span>Enter to open</span>
          <span>Esc to close</span>
        </p>
      </div>
    </Dialog>
  )
}
