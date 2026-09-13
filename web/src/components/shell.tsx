import { lazy, Suspense, useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useNavigate, useLocation } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { Menu, MenuItem, MenuSeparator } from '@hakopod/hatch-ui/components/dropdown-menu'
import { SettingsLayout } from '@hakopod/hatch-ui/blocks/settings-layout'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
  SheetBody,
} from '@hakopod/hatch-ui/components/sheet'
import { useLicense } from '../lib/license'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import type { Identity } from '../lib/types'
import { ScopeContext, canAccess, resolveWorkspaceScope } from '../lib/scope'
import { useTheme } from '../lib/appearance'
import { Avatar } from './avatar'
import { Logo, Icon } from './icons'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Tooltip } from './ui/surfaces'
import { Bot } from 'lucide-react'
import { Copy, Empty, ErrorState, Loading, Note } from './shared'
import { AuthScreen } from './auth-screen'

const CommandPalette = lazy(() => import('./command-palette'))
const ProjectWizard = lazy(() => import('./project-wizard'))
const WorkspaceGuidance = lazy(() => import('./workspace-guidance'))
const AppearanceSettings = lazy(() =>
  import('./appearance-settings').then((module) => ({ default: module.AppearanceSettings })),
)

export function DashboardShell({ children }: { children: ReactNode }) {
  const [mounted, setMounted] = useState(false)
  const [resetError, setResetError] = useState('')
  const [theme, setTheme] = useTheme()
  useEffect(() => setMounted(true), [])
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
  const toggleTheme = () => setTheme(theme === 'dark' ? 'light' : 'dark')
  if (!mounted || identity.isPending)
    return (
      <div className="hako-connection-page">
        <Logo size={32} />
        <h1>Connecting to your workspace</h1>
        <Loading rows={3} />
      </div>
    )
  if (identity.error && !(identity.error instanceof APIError && identity.error.status === 401))
    return (
      <div className="hako-connection-page">
        <Logo size={32} />
        <h1>Reconnect to Hakopod</h1>
        <p>The console couldn’t reach the management API.</p>
        <ErrorState error={identity.error} retry={() => void identity.refetch()} />
        {resetError && (
          <p className="hako-session-error" role="alert">
            {resetError}
          </p>
        )}
        <Button
          variant="outline"
          onClick={async () => {
            setResetError('')
            try {
              const response = await fetch('/session', { method: 'DELETE' })
              if (!response.ok) throw new Error('The session could not be cleared. Try again.')
              queryClient.clear()
              void identity.refetch()
            } catch (error) {
              setResetError(message(error))
            }
          }}
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
  const [preferencesOpen, setPreferencesOpen] = useState(false)
  const [assistantOpen, setAssistantOpen] = useState(false)
  const navigationTrigger = useRef<HTMLButtonElement>(null)
  const desktopNavigation = useRef<HTMLElement>(null)
  const accountTrigger = useRef<HTMLButtonElement>(null)
  const [sessionError, setSessionError] = useState('')
  const license = useLicense()
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        if (!commandOpen && document.querySelector('[role="dialog"]')) return
        setCommandOpen((open) => !open)
      }
    }
    const desktop = window.matchMedia('(min-width: 1200px)')
    const onResize = () => {
      if (desktop.matches) setMobileOpen(false)
    }
    window.addEventListener('keydown', key)
    desktop.addEventListener('change', onResize)
    return () => {
      window.removeEventListener('keydown', key)
      desktop.removeEventListener('change', onResize)
    }
  }, [commandOpen])
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { project: currentProject, environment } = resolveWorkspaceScope(projects.data?.items, {
    project: identity.project || selected.project,
    environment: identity.environment || selected.environment,
  })
  const project = currentProject?.name || ''
  const can = (permission: string) => canAccess(identity, project, permission)
  const changeScope = (next: { project: string; environment: string }) => {
    syncScope(next.project, next.environment)
    void navigate({ to: '/' })
    setMobileOpen(false)
  }
  const navigation = [
    { to: '/', icon: 'grid', label: 'Applications' },
    { to: '/templates', icon: 'box', label: 'Catalog' },
    { to: '/builds', icon: 'branch', label: 'Builds' },
    { to: '/infrastructure', icon: 'server', label: 'Infrastructure' },
    ...(identity.admin ? [{ to: '/backups', icon: 'archive', label: 'Backups' }] : []),
    { to: '/settings', icon: 'settings', label: 'Settings' },
  ]
  const isActive = (to: string) =>
    to === '/'
      ? location.pathname === '/' || /^\/(applications|deployments)(\/|$)/.test(location.pathname)
      : location.pathname === to || location.pathname.startsWith(to + '/')
  const accountRole = identity.owner
    ? 'Super admin'
    : identity.admin
      ? 'Installation administrator'
      : 'Scoped project access'
  const signOut = async () => {
    setSessionError('')
    try {
      const response = await fetch('/session', { method: 'DELETE' })
      if (!response.ok) throw new Error('Sign-out failed. Try again.')
      queryClient.clear()
      window.location.assign('/')
    } catch (error) {
      setSessionError(message(error))
    }
  }
  const links = (mobile = false) =>
    navigation.map(({ to, icon, label }) => (
      <Link
        key={to}
        to={to}
        className="hako-nav-link interactive"
        data-active={isActive(to) || undefined}
        aria-current={isActive(to) ? 'page' : undefined}
        onClick={() => setMobileOpen(false)}
      >
        {mobile && <Icon name={icon} size={18} />}
        <span>{label}</span>
        <Brackets />
      </Link>
    ))
  return (
    <ScopeContext.Provider value={{ project, environment, identity, can, syncScope }}>
      <a href="#main-content" className="skip-link">
        Skip to main content
      </a>
      <div className="hako-shell">
        <header className="hako-global-header">
          <div className="hako-brand-group">
            <Button
              variant="ghost"
              size="icon"
              className="hako-mobile-toggle"
              ref={navigationTrigger}
              aria-label="Open navigation"
              aria-controls="workspace-navigation"
              aria-expanded={mobileOpen}
              onClick={() => setMobileOpen(true)}
            >
              <Icon name="menu" />
            </Button>
            <Link to="/" className="hako-wordmark" aria-label="Hakopod applications">
              <img
                className="hako-wordmark-dark"
                src="/brand/hakopod-horizontal-paper.svg"
                alt=""
                width="124"
              />
              <img
                className="hako-wordmark-light"
                src="/brand/hakopod-horizontal-ink.svg"
                alt=""
                width="124"
              />
            </Link>
          </div>
          <div className="hako-scope-fields" role="group" aria-label="Workspace scope">
            <div className="hako-scope-select hako-project-select interactive">
              <select
                aria-label="Project"
                title={currentProject?.display_name || project || 'Select a project'}
                value={project}
                disabled={Boolean(identity.project)}
                onChange={(event) =>
                  changeScope({
                    project: event.target.value,
                    environment:
                      projects.data?.items.find((item) => item.name === event.target.value)
                        ?.environments?.[0]?.name || '',
                  })
                }
              >
                {!project && <option value="">Select a project</option>}
                {project && !currentProject && <option value={project}>{project}</option>}
                {projects.data?.items?.map((item) => (
                  <option key={item.id} value={item.name}>
                    {item.display_name || item.name}
                  </option>
                ))}
              </select>
              <Brackets />
            </div>
            <Icon name="chevron" size={12} />
            <div className="hako-scope-select hako-environment-select interactive">
              <select
                aria-label="Environment"
                title={environment || 'Select an environment'}
                value={environment}
                disabled={Boolean(identity.environment) || !project}
                onChange={(event) => changeScope({ project, environment: event.target.value })}
              >
                {!environment && <option value="">Select an environment</option>}
                {environment &&
                  !currentProject?.environments?.some((item) => item.name === environment) && (
                    <option value={environment}>{environment}</option>
                  )}
                {currentProject?.environments?.map((item) => (
                  <option key={item.name} value={item.name}>
                    {item.name}
                  </option>
                ))}
              </select>
              <Brackets />
            </div>
            {identity.admin && (
              <Tooltip content="Create a project">
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label="Create project"
                  onClick={() => setProjectOpen(true)}
                >
                  <Icon name="plus" size={16} />
                </Button>
              </Tooltip>
            )}
          </div>
          <nav className="hako-global-nav" aria-label="Main navigation" ref={desktopNavigation}>
            {links()}
          </nav>
          <div className="hako-header-tools">
            <Tooltip content="Quick navigation · ⌘ K">
              <Button
                variant="ghost"
                size="icon"
                className="hako-command-trigger"
                aria-label="Quick navigation"
                onClick={() => setCommandOpen(true)}
              >
                <Icon name="search" />
              </Button>
            </Tooltip>
            <a
              className="hako-docs-link interactive"
              href="https://github.com/hakopod/hakopod/blob/main/docs/cockpit.md"
              target="_blank"
              rel="noreferrer"
            >
              Docs
              <Brackets />
            </a>
            <Tooltip content="Workspace assistant">
              <Button
                variant="ghost"
                size="icon"
                aria-label="Open assistant"
                aria-expanded={assistantOpen}
                onClick={() => setAssistantOpen(true)}
              >
                <Bot size={19} strokeWidth={1.75} aria-hidden="true" />
              </Button>
            </Tooltip>
            <Menu
              className="hako-account-menu"
              trigger={
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label="Account menu"
                  ref={accountTrigger}
                  className="hako-account-trigger"
                >
                  <Avatar name={identity.name || 'Member'} url={identity.avatar_url} size={28} />
                </Button>
              }
            >
              <div className="hako-account-identity">
                <div>
                  <Avatar name={identity.name || 'Member'} url={identity.avatar_url} size={28} />
                  <strong>{identity.name || 'Member'}</strong>
                </div>
                {identity.email && <p>{identity.email}</p>}
                <small>{accountRole}</small>
              </div>
              <MenuItem onSelect={() => setPreferencesOpen(true)}>
                <Icon name="user" />
                Personal preferences
              </MenuItem>
              <MenuItem
                onSelect={() => void navigate({ to: '/settings', search: { tab: 'account' } })}
              >
                <Icon name="shield" />
                Account security
              </MenuItem>
              {identity.admin && (
                <MenuItem
                  onSelect={() => void navigate({ to: '/settings', search: { tab: 'keys' } })}
                >
                  <Icon name="key" />
                  API keys
                </MenuItem>
              )}
              <MenuItem
                onSelect={() => void navigate({ to: '/settings', search: { tab: 'license' } })}
              >
                <Icon name="info" />
                {license.data?.plan
                  ? `Hakopod ${license.data.plan === 'pro' ? 'Pro' : 'Free'}`
                  : 'Installation license'}
              </MenuItem>
              <MenuItem onSelect={toggleTheme}>
                <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
                Use {theme === 'dark' ? 'light' : 'dark'} theme
              </MenuItem>
              <MenuSeparator />
              <MenuItem onSelect={() => void signOut()}>
                <Icon name="logout" />
                Sign out
              </MenuItem>
            </Menu>
          </div>
        </header>
        <main
          id="main-content"
          tabIndex={-1}
          className={`page-content hako-page-content${location.pathname === '/settings' ? ' hako-settings-main' : ''}`}
        >
          {sessionError && (
            <div className="hako-session-error" role="alert">
              {sessionError}
            </div>
          )}
          {projects.error && (
            <ErrorState error={projects.error} retry={() => void projects.refetch()} />
          )}
          {!project && projects.isPending ? (
            <Loading />
          ) : !project &&
            !projects.error &&
            !location.pathname.startsWith('/settings') &&
            !location.pathname.startsWith('/login/') ? (
            <Empty
              icon="box"
              title="Create your first project"
              description="Projects organize applications and environments, with access scoped to your team."
              action={
                identity.admin ? (
                  <Button variant="primary" onClick={() => setProjectOpen(true)}>
                    <Icon name="plus" />
                    Create project
                  </Button>
                ) : (
                  <Note>Ask your administrator to create a project and invite you to it.</Note>
                )
              }
            />
          ) : (
            children
          )}
        </main>
        <footer className="hako-footer">
          <span>Hakopod · Infrastructure you own</span>
          <span>Self-hosted · API v1</span>
        </footer>
      </div>
      <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
        <SheetContent
          className="hako-mobile-sheet"
          onCloseAutoFocus={(event) => {
            event.preventDefault()
            if (navigationTrigger.current?.getClientRects().length)
              navigationTrigger.current.focus()
            else {
              const link =
                desktopNavigation.current?.querySelector<HTMLAnchorElement>(
                  'a[aria-current="page"]',
                ) || desktopNavigation.current?.querySelector<HTMLAnchorElement>('a')
              link?.focus()
            }
          }}
        >
          <SheetHeader>
            <SheetTitle>Navigation</SheetTitle>
            <SheetDescription>Applications and installation controls.</SheetDescription>
          </SheetHeader>
          <SheetBody>
            <nav
              id="workspace-navigation"
              className="hako-mobile-links"
              aria-label="Workspace navigation"
            >
              {links(true)}
              <a
                className="hako-nav-link interactive"
                href="https://github.com/hakopod/hakopod/blob/main/docs/cockpit.md"
                target="_blank"
                rel="noreferrer"
                onClick={() => setMobileOpen(false)}
              >
                <Icon name="book" size={18} />
                <span>Documentation</span>
                <Brackets />
              </a>
            </nav>
          </SheetBody>
        </SheetContent>
      </Sheet>
      {commandOpen && (
        <Suspense fallback={null}>
          <CommandPalette
            open={commandOpen}
            onOpenChange={setCommandOpen}
            project={project}
            environment={environment}
            onPreferences={() => setPreferencesOpen(true)}
          />
        </Suspense>
      )}
      {preferencesOpen && (
        <Preferences
          identity={identity}
          open={preferencesOpen}
          onOpenChange={setPreferencesOpen}
          restoreFocus={() => accountTrigger.current?.focus()}
        />
      )}
      {identity.admin && projectOpen && (
        <Suspense fallback={null}>
          <ProjectWizard
            open={projectOpen}
            onOpenChange={setProjectOpen}
            onCreated={(name, env) => {
              void queryClient.invalidateQueries({ queryKey: ['projects'] })
              changeScope({ project: name, environment: env })
            }}
          />
        </Suspense>
      )}
      {assistantOpen && (
        <Suspense fallback={null}>
          <WorkspaceGuidance
            open={assistantOpen}
            onOpenChange={setAssistantOpen}
            onCreateProject={() => setProjectOpen(true)}
            onCommands={() => setCommandOpen(true)}
          />
        </Suspense>
      )}
    </ScopeContext.Provider>
  )
}

function Preferences({
  identity,
  open,
  onOpenChange,
  restoreFocus,
}: {
  identity: Identity
  open: boolean
  onOpenChange: (open: boolean) => void
  restoreFocus: () => void
}) {
  const [section, setSection] = useState('profile')
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Personal preferences"
      description="Your account and browser preferences."
      onCloseAutoFocus={(event) => {
        event.preventDefault()
        restoreFocus()
      }}
      wide
    >
      <div className="hako-preferences">
        <SettingsLayout
          sections={[
            { id: 'profile', label: 'Profile', group: 'Personal' },
            { id: 'appearance', label: 'Appearance' },
          ]}
          active={section}
          onSectionChange={setSection}
          label="Personal preference sections"
        >
          {section === 'appearance' ? (
            <Suspense fallback={<Loading rows={2} />}>
              <AppearanceSettings />
            </Suspense>
          ) : (
            <div className="hako-preferences-profile">
              <div className="hako-profile-summary">
                <Avatar name={identity.name || 'Member'} url={identity.avatar_url} size={40} />
                <div>
                  <h2>{identity.name || 'Member'}</h2>
                  {identity.email && <p>{identity.email}</p>}
                </div>
              </div>
              <dl className="hako-profile-details">
                <div>
                  <dt>Account ID</dt>
                  <dd>
                    <code>{identity.id}</code>
                    <Copy value={identity.id} label="Copy ID" />
                  </dd>
                </div>
                <div>
                  <dt>Access</dt>
                  <dd>
                    {identity.owner
                      ? 'Super admin'
                      : identity.admin
                        ? 'Installation administrator'
                        : 'Scoped project access'}
                  </dd>
                </div>
              </dl>
              <div className="hako-preferences-actions">
                <Button variant="primary" asChild>
                  <Link to="/settings/profile" onClick={() => onOpenChange(false)}>
                    Edit profile
                  </Link>
                </Button>
                <Button variant="outline" asChild>
                  <Link
                    to="/settings"
                    search={{ tab: 'account' }}
                    onClick={() => onOpenChange(false)}
                  >
                    Account security
                  </Link>
                </Button>
              </div>
            </div>
          )}
        </SettingsLayout>
      </div>
    </Dialog>
  )
}
