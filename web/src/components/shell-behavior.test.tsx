import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterContextProvider,
} from '@tanstack/react-router'
import {
  accentForeground,
  accentPresets,
  accentText,
  contrastRatio,
  validAccent,
} from '../lib/appearance-color'
import { setAccent, setTheme } from '../lib/appearance'
import { projectIDFromName, projectIDPattern, projectInputErrors } from '../lib/project-input'
import { canAccess, resolveWorkspaceScope, ScopeContext } from '../lib/scope'
import type { Identity, Project } from '../lib/types'
import TeamSettings from './team-settings'

test('switching accounts discards saved projects and environments outside the current access list', () => {
  const projects: Project[] = [
    { id: 'one', name: 'personal', environments: [{ name: 'development' }] },
    { id: 'two', name: 'team', environments: [{ name: 'staging' }, { name: 'production' }] },
  ]
  const previousAccount = { project: 'old-project', environment: 'production' }
  assert.deepEqual(resolveWorkspaceScope(projects, previousAccount), {
    project: projects[0],
    environment: 'development',
  })
  assert.deepEqual(
    resolveWorkspaceScope(projects, { project: 'team', environment: 'production' }),
    {
      project: projects[1],
      environment: 'production',
    },
  )
  assert.deepEqual(resolveWorkspaceScope(projects, { project: 'team', environment: 'removed' }), {
    project: projects[1],
    environment: 'staging',
  })
  for (const unavailable of [undefined, []]) {
    assert.deepEqual(resolveWorkspaceScope(unavailable, previousAccount), {
      project: undefined,
      environment: '',
    })
  }
})

test('personal deployment and Free team creation never grant personal workspace sharing', () => {
  const personal: Identity = {
    id: 'personal-owner',
    name: 'Personal owner',
    admin: false,
    owner: false,
    credential_type: 'browser',
    permissions: ['admin'],
    project: '',
    environment: '',
    project_roles: [{ project: 'personal-project', role: 'admin' }],
  }
  assert.equal(canAccess(personal, 'personal-project', 'deployments:write'), true)
  assert.equal(canAccess(personal, 'personal-project', 'logs:read'), true)
  assert.equal(canAccess(personal, 'another-project', 'deployments:write'), false)
  assert.equal(canAccess(personal, 'personal-project', 'nodes:write'), false)

  for (const admin of [false, true]) {
    for (const pro of [false, true]) {
      for (const personalProject of [false, true]) {
        const identity = { ...personal, admin }
        const scenario = `admin=${admin} Pro=${pro} personal=${personalProject}`
        const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
        cache.setQueryData(['license'], {
          catalog: ['teams', 'invitations', 'project_rbac'].map((id) => ({ id, enabled: true })),
        })
        cache.setQueryData(['teams'], {
          items: [{ id: 'fixture-team', name: 'Fixture team', role: 'member' }],
        })
        cache.setQueryData(['team-members', 'fixture-team'], { items: [] })
        cache.setQueryData(['project-members', 'personal-project'], { items: [] })
        cache.setQueryData(['projects'], {
          items: [{ name: 'personal-project', personal: personalProject, environments: [] }],
        })
        const router = createRouter({
          routeTree: createRootRoute(),
          history: createMemoryHistory({ initialEntries: ['/settings'] }),
        })
        try {
          const html = renderToStaticMarkup(
            <QueryClientProvider client={cache}>
              <RouterContextProvider router={router}>
                <ScopeContext.Provider
                  value={{
                    project: 'personal-project',
                    environment: 'development',
                    identity,
                    can: (permission) => canAccess(identity, 'personal-project', permission),
                    syncScope: () => {},
                  }}
                >
                  <TeamSettings />
                </ScopeContext.Provider>
              </RouterContextProvider>
            </QueryClientProvider>,
          )
          const buttons = [...html.matchAll(/<button\b[^>]*>[\s\S]*?<\/button>/g)].map(
            ([button]) => button,
          )
          const create = buttons.find((button) => button.includes('Create team'))
          assert.ok(create, 'team settings renders its creation control')
          assert.equal(/\bdisabled=""/.test(create), !admin, scenario)
          const invitation = buttons.find((button) => button.includes('Invite to project'))
          assert.equal(Boolean(invitation), !personalProject, scenario)
          if (invitation) assert.equal(/\bdisabled=""/.test(invitation), false, scenario)
          assert.equal(html.includes('Grant a team access'), !personalProject, scenario)
          assert.equal(
            html.includes('Personal workspaces cannot be shared'),
            personalProject,
            scenario,
          )
        } finally {
          cache.clear()
        }
      }
    }
  }
})

test('readable project IDs preserve names separately and satisfy the scope contract', () => {
  assert.equal(projectIDFromName('Café Storefront'), 'cafe-storefront')
  assert.equal(projectIDFromName('2026 launch'), 'project-2026-launch')
  for (const name of ['Web', 'Café Storefront', '2026 launch', 'project '.repeat(20)]) {
    const id = projectIDFromName(name)
    assert.ok(projectIDPattern.test(id))
    assert.ok(id.length <= 40)
  }
  const input = {
    displayName: 'My Storefront',
    id: 'storefront',
    description: 'A storefront',
    environment: 'production',
  }
  assert.equal(Object.values(projectInputErrors(input)).some(Boolean), false)
  assert.equal(projectInputErrors({ ...input, displayName: '  ' }).displayName, 'Required')
  assert.ok(projectInputErrors({ ...input, id: 'Invalid ID' }).id)
  assert.ok(projectInputErrors({ ...input, environment: 'production-' }).environment)
  assert.ok(projectInputErrors({ ...input, description: 'x'.repeat(1001) }).description)
  assert.equal(projectInputErrors({ ...input, displayName: 'a'.repeat(80) }).displayName, '')
  assert.ok(projectInputErrors({ ...input, displayName: 'a'.repeat(81) }).displayName)
})

test('custom accent text remains readable on buttons and both theme surfaces', () => {
  const colors = [
    ...accentPresets.map((preset) => preset.color),
    '#000000',
    '#FFFFFF',
    '#777777',
    '#767676',
    '#808000',
    '#0000FF',
    '#FF0000',
    '#00FF00',
    '#123456',
  ]
  for (const color of colors) {
    assert.ok(contrastRatio(color, accentForeground(color)) >= 4.5, `button ${color}`)
    assert.ok(contrastRatio(accentText(color, 'dark'), '#1D2C26') >= 4.5, `Ink ${color}`)
    assert.ok(contrastRatio(accentText(color, 'light'), '#D9DFD4') >= 4.5, `Paper ${color}`)
  }
  for (const color of ['red', '#123', '#1234567', '#GGGGGG', 'url(example)'])
    assert.equal(validAccent(color), false)
})

test('accent persistence failures keep the selected color and theme changes keep its contrast', () => {
  const originals = Object.fromEntries(
    ['document', 'window', 'localStorage'].map((key) => [
      key,
      Object.getOwnPropertyDescriptor(globalThis, key),
    ]),
  )
  const properties = new Map<string, string>()
  const stored = new Map<string, string>()
  const classes = new Set(['dark'])
  const root = {
    dataset: { theme: 'dark', accent: '' },
    style: {
      setProperty: (name: string, value: string) => properties.set(name, value),
      removeProperty: (name: string) => properties.delete(name),
    },
    classList: {
      add: (name: string) => classes.add(name),
      remove: (...names: string[]) => names.forEach((name) => classes.delete(name)),
    },
  }
  let blocked = false
  Object.defineProperties(globalThis, {
    document: { configurable: true, value: { documentElement: root } },
    window: { configurable: true, value: new EventTarget() },
    localStorage: {
      configurable: true,
      value: {
        setItem: (name: string, value: string) => {
          if (blocked) throw new Error('storage unavailable')
          stored.set(name, value)
        },
      },
    },
  })
  try {
    assert.equal(setAccent('#123456'), true)
    assert.equal(stored.get('hakopod-accent'), '#123456')
    setTheme('light')
    assert.equal(root.dataset.theme, 'light')
    assert.deepEqual([...classes], ['light'])
    assert.equal(root.dataset.accent, '#123456')
    assert.equal(properties.get('--action-text'), accentText('#123456', 'light'))
    blocked = true
    assert.equal(setAccent('#abcdef'), false)
    assert.equal(root.dataset.accent, '#ABCDEF')
    assert.equal(properties.get('--action'), '#ABCDEF')
    assert.equal(setAccent('invalid'), false)
    assert.equal(root.dataset.accent, '#ABCDEF')
  } finally {
    for (const [key, descriptor] of Object.entries(originals)) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else Reflect.deleteProperty(globalThis, key)
    }
  }
})
