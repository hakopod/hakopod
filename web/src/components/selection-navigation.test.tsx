import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { parentNavigation } from '../lib/navigation'
import { canCreateEnvironment } from '../lib/scope'
import type { Identity } from '../lib/types'
import { SelectField } from './ui/select'

test('select fields preserve an empty choice and submitted external values', () => {
  for (const value of ['', 'option-0', 'private-registry']) {
    const label = value === '' ? 'No registry' : value
    const html = renderToStaticMarkup(
      <form>
        <SelectField
          name="registry"
          label="Registry credential"
          value={value}
          onValueChange={() => {}}
          options={[
            { value: '', label: 'No registry' },
            { value: 'option-0', label: 'option-0' },
            { value: 'private-registry', label: 'private-registry' },
          ]}
        />
      </form>,
    )
    const trigger = html.match(/<button\b[^>]*>[\s\S]*?<\/button>/)?.[0] || ''
    assert.match(trigger, /type="button"/)
    assert.match(trigger, /role="combobox"/)
    assert.match(trigger, /aria-label="Registry credential"/)
    assert.ok(trigger.includes(`>${label}<`), 'the selected label is available before hydration')
    const submitted = html.match(/<input\b[^>]*name="registry"[^>]*>/)?.[0] || ''
    assert.ok(submitted.includes(`value="${value}"`), 'form submission preserves the API value')
    assert.equal((html.match(/name="registry"/g) || []).length, 1)
  }
})

test('required empty selection and disabled fields retain form semantics', () => {
  for (const disabled of [false, true]) {
    const html = renderToStaticMarkup(
      <form>
        <SelectField
          label="Database"
          name="database"
          value=""
          onValueChange={() => {}}
          required
          disabled={disabled}
          options={[
            { value: '', label: 'Choose a database' },
            { value: 'unavailable', label: 'Database unavailable', disabled: true },
          ]}
        />
      </form>,
    )
    const trigger = html.match(/<button\b[^>]*>/)?.[0] || ''
    const formControl = html.match(/<select\b[^>]*>/)?.[0] || ''
    assert.match(trigger, /aria-required="true"/)
    assert.match(trigger, /data-placeholder=""/)
    assert.match(formControl, /required=""/)
    assert.equal(trigger.includes('disabled=""'), disabled)
    assert.equal(formControl.includes('disabled=""'), disabled)
    const submitted = html.match(/<input\b[^>]*name="database"[^>]*>/)?.[0] || ''
    assert.equal(submitted.includes('disabled=""'), disabled)
  }
})

test('parent navigation uses route context without relying on browser history', () => {
  assert.equal(parentNavigation('/'), null)
  assert.equal(parentNavigation('/networks'), null)
  assert.deepEqual(parentNavigation('/applications/example', { service: 'worker', tab: 'logs' }), {
    to: '/applications/example',
    label: 'Back to application',
    search: { tab: 'services' },
  })
  for (const route of ['configure', 'environment']) {
    assert.deepEqual(parentNavigation(`/applications/example/${route}`, { service: 'worker' }), {
      to: '/applications/example',
      label: 'Back to service',
      search: { tab: route === 'environment' ? 'environment' : 'settings', service: 'worker' },
    })
  }
  assert.deepEqual(parentNavigation('/applications/example/domains', { service: 'web' }), {
    to: '/applications/example',
    label: 'Back to service',
    search: { tab: 'network', service: 'web' },
  })
  assert.deepEqual(parentNavigation('/applications/example/domains'), {
    to: '/applications/example',
    label: 'Back to application',
    search: { tab: 'networking' },
  })
  assert.deepEqual(parentNavigation('/applications/example/source'), {
    to: '/applications/example',
    label: 'Back to application',
    search: { tab: 'source' },
  })
  assert.deepEqual(parentNavigation('/deployments/release', {}, 'example'), {
    to: '/applications/example',
    label: 'Back to application',
    search: { tab: 'deployments' },
  })
  assert.deepEqual(parentNavigation('/deployments/release'), {
    to: '/',
    label: 'Back to projects',
  })
  assert.deepEqual(parentNavigation('/networks/private/connect'), {
    to: '/networks/private',
    label: 'Back to network',
  })
  for (const route of ['/networks/private', '/networks/new'])
    assert.deepEqual(parentNavigation(route), { to: '/networks', label: 'Back to networks' })
  assert.deepEqual(parentNavigation('/builds/source/edit'), {
    to: '/builds/source',
    label: 'Back to build',
  })
  assert.deepEqual(parentNavigation('/builds/new', { application: 'example' }), {
    to: '/applications/example',
    label: 'Back to application',
    search: { tab: 'source' },
  })
  for (const tab of ['destinations', 'schedules', 'artifacts'])
    assert.deepEqual(parentNavigation(`/backups/${tab}/example/edit`), {
      to: '/backups',
      label: 'Back to backups',
      search: { tab },
    })
  assert.deepEqual(parentNavigation('/infrastructure/nodes/worker/terminal'), {
    to: '/infrastructure',
    label: 'Back to infrastructure',
    search: { tab: 'nodes' },
  })
  assert.deepEqual(parentNavigation('/settings/integrations/github'), {
    to: '/settings/integrations',
    label: 'Back to integrations',
  })
  assert.deepEqual(
    parentNavigation('/templates/database', { q: 'data', category: 'database', token: 'omit' }),
    {
      to: '/templates',
      label: 'Back to catalog',
      search: { q: 'data', category: 'database' },
    },
  )
})

test('project navigation returns to its URL scope and has a safe unscoped fallback', () => {
  assert.deepEqual(parentNavigation('/projects/production', { environment: 'staging' }), {
    to: '/',
    label: 'Back to projects',
  })
  for (const path of ['/applications/example', '/applications/new', '/applications/import']) {
    assert.deepEqual(
      parentNavigation(path, {}, undefined, { project: 'team/a', environment: 'staging' }),
      {
        to: '/projects/team%2Fa',
        label: 'Back to applications',
        search: { environment: 'staging' },
      },
    )
    assert.deepEqual(parentNavigation(path), { to: '/', label: 'Back to projects' })
  }
})

test('environment creation is available to project owners but never scoped viewers or developers', () => {
  const identity: Identity = {
    id: 'member',
    name: 'Member',
    admin: false,
    owner: false,
    credential_type: 'browser',
    permissions: ['admin'],
    project: '',
    environment: '',
    project_roles: [{ project: 'personal', role: 'admin' }],
  }
  assert.equal(canCreateEnvironment(identity, 'personal'), true)
  assert.equal(canCreateEnvironment(identity, 'unrelated'), false)
  assert.equal(canCreateEnvironment({ ...identity, admin: true }, 'unrelated'), true)
  for (const role of ['developer', 'viewer'])
    assert.equal(
      canCreateEnvironment(
        { ...identity, project_roles: [{ project: 'personal', role }] },
        'personal',
      ),
      false,
    )
  assert.equal(canCreateEnvironment({ ...identity, project: 'other' }, 'personal'), false)
  assert.equal(canCreateEnvironment({ ...identity, environment: 'development' }, 'personal'), false)
  assert.equal(canCreateEnvironment({ ...identity, application: 'example' }, 'personal'), false)
  assert.equal(canCreateEnvironment({ ...identity, credential_type: 'machine' }, 'personal'), false)
  assert.equal(canCreateEnvironment(identity, ''), false)
})
