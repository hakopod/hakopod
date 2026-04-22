import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import * as DropdownMenu from '@radix-ui/react-dropdown-menu'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import type { Identity } from '../lib/types'
import { Logo, Icon } from './icons'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { ErrorState, Note } from './shared'

import { ScopeContext } from '../lib/scope'

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
          Use a different API key
        </Button>
      </div>
    )
  if (!identity.data)
    return (
      <Login
        onSuccess={() => void queryClient.invalidateQueries({ queryKey: ['me'] })}
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

function Login({
  onSuccess,
  toggleTheme,
  theme,
}: {
  onSuccess: () => void
  toggleTheme: () => void
  theme: string
}) {
  const [token, setToken] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  async function signIn(event: FormEvent) {
    event.preventDefault()
    setError('')
    setBusy(true)
    try {
      const response = await fetch('/session', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: token.trim() }),
      })
      const payload = await response.json()
      if (!response.ok) throw new Error(payload.error?.message || 'Unable to sign in.')
      setToken('')
      onSuccess()
    } catch (error) {
      setError(message(error))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="login-page">
      <div className="login-brand-panel">
        <div className="brand">
          <Logo />
          <span>
            hakopod<span className="brand-period">.</span>
          </span>
        </div>
        <div className="login-pitch">
          <span className="label-chip">
            <span className="status-dot" /> BUILT FOR YOUR INFRASTRUCTURE
          </span>
          <h1>
            Your next idea.
            <br />
            <span>Your own cloud.</span>
          </h1>
          <p>
            Bring your containers. Keep control.
            <br />A calmer place to build, deploy, and grow.
          </p>
          <div className="network-illustration" aria-hidden="true">
            <div className="network-line network-line-one" />
            <div className="network-line network-line-two" />
            <div className="illustration-node node-top">
              <Icon name="box" size={26} />
              <span>Your application</span>
            </div>
            <div className="illustration-node node-left">
              <Icon name="globe" />
              <span>Public web</span>
            </div>
            <div className="illustration-node node-right">
              <Icon name="lock" />
              <span>Private API</span>
            </div>
            <div className="illustration-caption">
              <Icon name="shield" size={14} /> Connected on your private network
            </div>
          </div>
        </div>
        <div className="login-footer">Self-hosted. Open source. Yours.</div>
      </div>
      <div className="login-form-panel">
        <Button
          variant="ghost"
          size="icon"
          className="login-theme"
          aria-label="Toggle color theme"
          onClick={toggleTheme}
        >
          <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
        </Button>
        <div className="login-form-content">
          <div className="login-symbol">
            <Icon name="key" size={23} />
          </div>
          <h2>Welcome to your workspace</h2>
          <p>Connect to this Hakopod installation with your API key.</p>
          <form onSubmit={signIn}>
            <label htmlFor="api-key">API key</label>
            <input
              id="api-key"
              type="password"
              placeholder="Paste your API key"
              autoComplete="off"
              value={token}
              onChange={(event) => setToken(event.target.value)}
              required
              minLength={16}
              maxLength={512}
              autoFocus
            />
            <p className="field-help">Your key’s permissions determine what you can access.</p>
            {error && (
              <div className="inline-error" role="alert">
                {error}
              </div>
            )}
            <Button
              variant="primary"
              className="full-width"
              type="submit"
              disabled={busy || !token.trim()}
            >
              {busy ? 'Connecting…' : 'Open workspace'}
              <Icon name="arrow" size={17} />
            </Button>
          </form>
          <div className="login-assurance">
            <Icon name="lock" size={14} />
            <span>Your key stays in an encrypted, HttpOnly session.</span>
          </div>
          <div className="setup-help">
            <h3>Setting up for the first time?</h3>
            <p>
              Start the management API and use the administrator key from your installation’s
              bootstrap process. Configure the application domain and cluster before deploying.
            </p>
          </div>
        </div>
        <div className="login-bottom">
          Hakopod <span>Milestone 1 · Deployment foundation</span>
        </div>
      </div>
    </div>
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
  const projects = useQuery({
    queryKey: ['projects'],
    queryFn: ({ signal }) => unwrap(client.GET('/projects', { signal })),
    staleTime: 60000,
  })
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
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const project = identity.project || selected.project || projects.data?.items?.[0]?.name || ''
  const currentProject = projects.data?.items?.find((p) => p.name === project)
  const environment =
    identity.environment || selected.environment || currentProject?.environments?.[0]?.name || ''
  const can = (permission: string) => identity.admin || identity.permissions?.includes(permission)
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
        <aside className={`sidebar ${mobileOpen ? 'sidebar-open' : ''}`}>
          <Link className="brand" to="/">
            <Logo size={28} />
            <span>
              hakopod<span className="brand-period">.</span>
            </span>
          </Link>
          <div className="workspace-card">
            <span className="workspace-avatar">
              {(identity.name || 'W').slice(0, 1).toUpperCase()}
            </span>
            <div>
              <strong>Your workspace</strong>
              <span>Self-hosted infrastructure</span>
            </div>
            <Icon name="lock" size={13} />
          </div>
          <div className="nav-label">WORKSPACE</div>
          <nav aria-label="Main navigation">
            <Link
              to="/"
              className="nav-item"
              activeProps={{ className: 'nav-item active' }}
              activeOptions={{ exact: true }}
              onClick={() => setMobileOpen(false)}
            >
              <Icon name="grid" />
              <span>Applications</span>
            </Link>
            <Link
              to="/infrastructure"
              className="nav-item"
              activeProps={{ className: 'nav-item active' }}
              onClick={() => setMobileOpen(false)}
            >
              <Icon name="server" />
              <span>Infrastructure</span>
            </Link>
            {identity.admin && (
              <Link
                to="/settings"
                className="nav-item"
                activeProps={{ className: 'nav-item active' }}
                onClick={() => setMobileOpen(false)}
              >
                <Icon name="settings" />
                <span>Administration</span>
              </Link>
            )}
          </nav>
          <div className="sidebar-spacer" />
          <div className="ownership-note">
            <Icon name="shield" size={20} />
            <strong>Runs on your terms.</strong>
            <p>
              Your machines.
              <br />
              Your applications. Your data.
            </p>
          </div>
          <div className="sidebar-bottom">
            <div className="user-avatar">{(identity.name || 'K').slice(0, 1).toUpperCase()}</div>
            <div className="user-meta">
              <strong>{identity.name || 'API key session'}</strong>
              <span>{identity.admin ? 'Administrator' : 'Scoped access'}</span>
            </div>
            <DropdownMenu.Root>
              <DropdownMenu.Trigger asChild>
                <Button variant="ghost" size="icon" aria-label="Account menu">
                  <Icon name="down" size={15} />
                </Button>
              </DropdownMenu.Trigger>
              <DropdownMenu.Portal>
                <DropdownMenu.Content className="dropdown-menu" sideOffset={8} align="end">
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
          </div>
        </aside>
        {mobileOpen && (
          <button
            className="sidebar-backdrop"
            aria-label="Close navigation"
            onClick={() => setMobileOpen(false)}
          />
        )}
        <div className="app-main">
          <header className="topbar">
            <Button
              variant="ghost"
              size="icon"
              className="mobile-menu"
              aria-label="Open navigation"
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
            {!project && !projects.isPending && !projects.error ? (
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
                  <Note>
                    Ask your administrator to create a project and grant your API key access.
                  </Note>
                )}
              </div>
            ) : (
              children
            )}
          </main>
          <footer className="app-footer">
            <span>Hakopod · Infrastructure you own</span>
            <span>
              API v1 <span className="footer-dot">·</span> Deployment foundation
            </span>
          </footer>
        </div>
      </div>
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
