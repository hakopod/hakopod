import { Avatar } from './avatar'
import { lazy, Suspense, useCallback, useEffect, useState, type ReactNode } from 'react'
import { Badge, Tooltip } from '@hakopod/ui'
import { useLicense } from '../lib/license'
const CommandPalette = lazy(() => import('./command-palette'))
import { Link, useNavigate, useLocation } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import * as DropdownMenu from '@radix-ui/react-dropdown-menu'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import type { Identity } from '../lib/types'
import { Logo, Icon } from './icons'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { ErrorState, Note } from './shared'

import { ScopeContext, canAccess } from '../lib/scope'
import { applyAccent } from '../lib/appearance'
import { AuthScreen } from './auth-screen'

export function DashboardShell({ children }: { children: ReactNode }) {
  const [mounted, setMounted] = useState(false)
  const [theme, setTheme] = useState('dark')
  useEffect(() => {
    setMounted(true)
    setTheme(document.documentElement.dataset.theme || 'dark')
  }, [])
  const identity = useQuery({
    queryKey: ['me'],
    queryFn: ({ signal }) => unwrap(client.GET('/me', { signal })),
    enabled: mounted,
    retry: false,
    staleTime: 30000,
  })
  const queryClient = useQueryClient()
  useEffect(() => {
    if (identity.data?.credential_type !== 'browser') return
    const destination = sessionStorage.getItem('hakopod-auth-return')
    if (destination?.startsWith('/login/')) {
      sessionStorage.removeItem('hakopod-auth-return')
      if (destination !== window.location.pathname + window.location.search)
        window.location.assign(destination)
    }
  }, [identity.data?.credential_type])
  const toggleTheme = () => {
    const next = theme === 'dark' ? 'light' : 'dark'
    setTheme(next)
    document.documentElement.dataset.theme = next
    try {
      localStorage.setItem('hakopod-theme', next)
    } catch {}
  }
  if (!mounted || identity.isPending)
    return (
      <div className="boot-state">
        <Logo size={40} />
        <p>Connecting to your workspace…</p>
        <div className="boot-line" />
      </div>
    )
  if (identity.error && !(identity.error instanceof APIError && identity.error.status === 401))
    return (
      <div className="connection-page">
        <Logo size={42} />
        <h1>Let’s reconnect.</h1>
        <p>Your dashboard couldn’t reach the management API.</p>
        <ErrorState error={identity.error} retry={() => void identity.refetch()} />
        <Button
          onClick={() =>
            void fetch('/session', { method: 'DELETE' }).then(() => {
              queryClient.clear()
              void identity.refetch()
            })
          }
        >
          Sign in again
        </Button>
      </div>
    )
  if (!identity.data || identity.data.credential_type !== 'browser')
    return (
      <AuthScreen
        inviteToken={
          window.location.pathname === '/login/invite'
            ? new URLSearchParams(window.location.search).get('token') || ''
            : ''
        }
        onSuccess={() => {
          const destination = sessionStorage.getItem('hakopod-auth-return')
          sessionStorage.removeItem('hakopod-auth-return')
          if (destination?.startsWith('/login/')) window.location.assign(destination)
          else {
            void queryClient.invalidateQueries({ queryKey: ['me'] })
            void queryClient.invalidateQueries({ queryKey: ['auth-status'] })
          }
        }}
        toggleTheme={toggleTheme}
        theme={theme}
      />
    )
  return (
    <Workspace identity={identity.data} theme={theme} toggleTheme={toggleTheme}>
      {children}
    </Workspace>
  )
}

function Workspace({
  identity,
  children,
  theme,
  toggleTheme,
}: {
  identity: Identity
  children: ReactNode
  theme: string
  toggleTheme: () => void
}) {
  const location = useLocation()
  const projects = useQuery({
    queryKey: ['projects'],
    queryFn: ({ signal }) => unwrap(client.GET('/projects', { signal })),
    staleTime: 60000,
  })
  const appearance = useQuery({
    queryKey: ['appearance'],
    queryFn: ({ signal }) => unwrap(client.GET('/settings/appearance', { signal })),
    staleTime: 300000,
  })
  useEffect(() => {
    if (appearance.data) applyAccent(appearance.data.accent_color, theme)
  }, [appearance.data, theme])
  const [selected, setSelected] = useState(() => {
    try {
      const saved = JSON.parse(sessionStorage.getItem('hakopod-scope') || '{}')
      return {
        project: typeof saved.project === 'string' ? saved.project : '',
        environment: typeof saved.environment === 'string' ? saved.environment : '',
      }
    } catch {
      return { project: '', environment: '' }
    }
  })
  const syncScope = useCallback((project: string, environment: string) => {
    setSelected((current) =>
      current.project === project && current.environment === environment
        ? current
        : { project, environment },
    )
    try {
      sessionStorage.setItem('hakopod-scope', JSON.stringify({ project, environment }))
    } catch {}
  }, [])
  const [projectOpen, setProjectOpen] = useState(false)
  const [mobileOpen, setMobileOpen] = useState(false)
  const [commandOpen, setCommandOpen] = useState(false)
  const license = useLicense()
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setCommandOpen((open) => !open)
      }
    }
    window.addEventListener('keydown', key)
    return () => window.removeEventListener('keydown', key)
  }, [])
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const project = identity.project || selected.project || projects.data?.items?.[0]?.name || ''
  const currentProject = projects.data?.items?.find((p) => p.name === project)
  const environment =
    identity.environment || selected.environment || currentProject?.environments?.[0]?.name || ''
  const can = (permission: string) =>
    (identity.admin ||
      Boolean(license.data?.catalog.find((feature) => feature.id === 'project_rbac')?.enabled)) &&
    canAccess(identity, project, permission)
  const changeScope = (next: { project: string; environment: string }) => {
    syncScope(next.project, next.environment)
    void navigate({ to: '/' })
    setMobileOpen(false)
  }
  return (
    <ScopeContext.Provider value={{ project, environment, identity, can, syncScope }}>
      <a href="#main-content" className="skip-link">
        Skip to main content
      </a>
      <div className="app-shell">
        <aside
          id="workspace-navigation"
          className={`sidebar cockpit-rail ${mobileOpen ? 'sidebar-open' : ''}`}
        >
          <Tooltip content="Hakopod · Applications">
            <Link className="rail-brand" to="/" aria-label="Hakopod applications">
              <Logo size={32} />
            </Link>
          </Tooltip>
          <nav aria-label="Main navigation" className="rail-navigation">
            {(
              [
                ['/', 'grid', 'Applications'],
                ['/infrastructure', 'server', 'Infrastructure'],
                ['/builds', 'branch', 'Source builds'],
                ['/templates', 'box', 'Templates'],
                ...(identity.admin ? [['/backups', 'archive', 'Backups'] as const] : []),
                ['/settings', 'shield', 'Account & access'],
              ] as const
            ).map(([to, icon, label]) => (
              <Tooltip content={label} key={to}>
                <Link
                  to={to}
                  aria-label={label}
                  title={label}
                  className="nav-item rail-item"
                  activeProps={{ className: 'nav-item rail-item active' }}
                  activeOptions={{ exact: to === '/' }}
                  onClick={() => setMobileOpen(false)}
                >
                  <Icon name={icon} size={19} />
                  <span className="rail-label">{label}</span>
                </Link>
              </Tooltip>
            ))}
          </nav>
          <div className="sidebar-spacer" />
          <Tooltip content="Quick navigation · ⌘ K">
            <Button
              variant="ghost"
              size="icon"
              aria-label="Quick navigation"
              onClick={() => setCommandOpen(true)}
            >
              <Icon name="search" />
            </Button>
          </Tooltip>
          <Tooltip content={`Use ${theme === 'dark' ? 'light' : 'dark'} theme`}>
            <Button
              variant="ghost"
              size="icon"
              aria-label={`Use ${theme === 'dark' ? 'light' : 'dark'} theme`}
              onClick={toggleTheme}
            >
              <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
            </Button>
          </Tooltip>
          <DropdownMenu.Root>
            <Tooltip content={identity.name || 'Account'}>
              <DropdownMenu.Trigger asChild>
                <Button
                  className="rail-avatar"
                  variant="ghost"
                  size="icon"
                  aria-label="Account menu"
                >
                  <Avatar name={identity.name || 'Member'} url={identity.avatar_url} size={34} />
                </Button>
              </DropdownMenu.Trigger>
            </Tooltip>
            <DropdownMenu.Portal>
              <DropdownMenu.Content
                className="dropdown-menu"
                side="right"
                sideOffset={14}
                align="end"
              >
                <div className="account-menu-heading">
                  <strong>{identity.name || 'Member'}</strong>
                  <small>
                    {identity.owner
                      ? 'Super admin'
                      : identity.admin
                        ? 'Installation administrator'
                        : 'Scoped project access'}
                  </small>
                </div>
                <DropdownMenu.Item className="dropdown-item" onSelect={toggleTheme}>
                  <Icon name={theme === 'dark' ? 'sun' : 'moon'} size={16} />
                  Use {theme === 'dark' ? 'light' : 'dark'} theme
                </DropdownMenu.Item>
                <DropdownMenu.Item
                  className="dropdown-item"
                  onSelect={async () => {
                    await fetch('/session', { method: 'DELETE' })
                    queryClient.clear()
                    window.location.assign('/')
                  }}
                >
                  <Icon name="logout" size={16} />
                  Sign out
                </DropdownMenu.Item>
              </DropdownMenu.Content>
            </DropdownMenu.Portal>
          </DropdownMenu.Root>
        </aside>
        {mobileOpen && (
          <button
            className="sidebar-backdrop"
            aria-label="Close navigation"
            onClick={() => setMobileOpen(false)}
          />
        )}
        <div className="app-main">
          <header className="topbar cockpit-topbar">
            <Link to="/" className="brand-wordmark" aria-label="Hakopod">
              <img
                className="wordmark-dark"
                src="/brand/hakopod-horizontal-paper.svg"
                alt=""
                width="140"
              />
              <img
                className="wordmark-light"
                src="/brand/hakopod-horizontal-ink.svg"
                alt=""
                width="140"
              />
            </Link>
            <Button
              variant="ghost"
              size="icon"
              className="mobile-menu"
              aria-label="Open navigation"
              aria-controls="workspace-navigation"
              aria-expanded={mobileOpen}
              onClick={() => setMobileOpen(true)}
            >
              <Icon name="menu" />
            </Button>
            <div className="context-select">
              <Icon name="box" size={17} />
              <select
                aria-label="Project"
                value={project}
                onChange={(event) =>
                  changeScope({
                    project: event.target.value,
                    environment:
                      projects.data?.items.find((p) => p.name === event.target.value)
                        ?.environments?.[0]?.name || '',
                  })
                }
              >
                {!project && <option value="">Select a project</option>}
                {projects.data?.items?.map((p) => (
                  <option key={p.id} value={p.name}>
                    {p.name}
                  </option>
                ))}
              </select>
              <span className="context-slash">/</span>
              <span className="environment-dot" />
              <select
                aria-label="Environment"
                value={environment}
                onChange={(event) => changeScope({ project, environment: event.target.value })}
              >
                {!environment && <option value="">Environment</option>}
                {currentProject?.environments?.map((env) => (
                  <option key={env.name} value={env.name}>
                    {env.name}
                  </option>
                ))}
              </select>
              {identity.admin && (
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label="Create project"
                  title="Create project"
                  onClick={() => setProjectOpen(true)}
                >
                  <Icon name="plus" size={14} />
                </Button>
              )}
            </div>
            <div className="topbar-right">
              <button
                type="button"
                className="command-trigger"
                onClick={() => setCommandOpen(true)}
              >
                <Icon name="search" size={14} />
                <span>Find apps, resources…</span>
                <kbd>⌘ K</kbd>
              </button>
              <Link
                to="/settings"
                search={{ tab: 'license' }}
                className="license-header-link"
                aria-label="View installation license"
              >
                <Badge tone={license.data?.plan === 'pro' ? 'accent' : 'neutral'}>
                  {license.data?.plan
                    ? `Hakopod ${license.data.plan === 'pro' ? 'Pro' : 'Free'}`
                    : 'License…'}
                </Badge>
              </Link>
              <span className="installation-label">
                <span className="tiny-square" /> SELF-HOSTED
              </span>
              <span className="topbar-divider" />
              <Button
                variant="ghost"
                size="icon"
                aria-label={`Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`}
                onClick={toggleTheme}
              >
                <Icon name={theme === 'dark' ? 'sun' : 'moon'} size={18} />
              </Button>
            </div>
          </header>
          <main id="main-content" tabIndex={-1} className="page-content">
            {projects.error && (
              <ErrorState error={projects.error} retry={() => void projects.refetch()} />
            )}
            {!project &&
            !projects.isPending &&
            !projects.error &&
            !location.pathname.startsWith('/settings') &&
            !location.pathname.startsWith('/login/') ? (
              <div className="first-project">
                <div className="eyebrow">YOUR WORKSPACE IS READY</div>
                <h1>Make room for your next idea.</h1>
                <p>
                  Projects organize applications and environments, with access scoped to your team.
                </p>
                {identity.admin ? (
                  <Button variant="primary" onClick={() => setProjectOpen(true)}>
                    <Icon name="plus" />
                    Create your first project
                  </Button>
                ) : (
                  <Note>Ask your administrator to create a project and invite you to it.</Note>
                )}
              </div>
            ) : (
              children
            )}
          </main>
          <footer className="app-footer">
            <span>Hakopod · Infrastructure you own</span>
            <span>
              API v1 <span className="footer-dot">·</span> Your infrastructure
            </span>
          </footer>
        </div>
      </div>
      {commandOpen && (
        <Suspense fallback={null}>
          <CommandPalette
            open={commandOpen}
            onOpenChange={setCommandOpen}
            project={project}
            environment={environment}
          />
        </Suspense>
      )}
      <CreateProject
        open={projectOpen}
        setOpen={setProjectOpen}
        onCreated={(name, env) => {
          void queryClient.invalidateQueries({ queryKey: ['projects'] })
          changeScope({ project: name, environment: env })
        }}
      />
    </ScopeContext.Provider>
  )
}

function CreateProject({
  open,
  setOpen,
  onCreated,
}: {
  open: boolean
  setOpen: (v: boolean) => void
  onCreated: (name: string, environment: string) => void
}) {
  const [name, setName] = useState('')
  const [environment, setEnvironment] = useState('development')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      title="Create a project"
      description="A home for related applications and their environments."
    >
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          setBusy(true)
          setError('')
          try {
            await unwrap(client.POST('/projects', { body: { name, environment } }))
            onCreated(name, environment)
            setOpen(false)
            setName('')
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="dialog-body field-stack">
          <label>
            Project name
            <input
              required
              pattern="[a-z0-9][a-z0-9-]*"
              placeholder="my-project"
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </label>
          <label>
            Initial environment
            <input
              required
              pattern="[a-z0-9][a-z0-9-]*"
              value={environment}
              onChange={(event) => setEnvironment(event.target.value)}
            />
          </label>
          {error && <div className="inline-error">{error}</div>}
        </div>
        <div className="dialog-footer">
          <Button type="button" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Creating…' : 'Create project'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
