import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import {
  prepareProviderInput,
  providerConfiguration,
  sameProviderSource,
  scopeSummary,
  type SecretProvider,
} from '../lib/secret-providers'
import { parentNavigation } from '../lib/navigation'
import { SecretProviderReview } from './secret-provider-review'
import { SecretProviderScopes } from './secret-provider-scopes'

const credentials = {
  token: 'private-vault-token',
  client_id: 'private-client-id',
  client_secret: 'private-client-secret',
}
const configuration = {
  ...providerConfiguration(),
  name: 'vault',
  endpoint: 'https://vault.example.test',
  scopes: [{ project: 'demo', environments: [] }],
}

test('provider review displays access and source without credential values', () => {
  const input = prepareProviderInput(configuration, '10.20.0.0/24', credentials, true, 0)
  const { credentials: hidden, ...publicConfiguration } = input
  const html = renderToStaticMarkup(
    <SecretProviderReview configuration={publicConfiguration} replacingCredentials />,
  )
  assert.equal(hidden?.token, credentials.token)
  assert.ok(html.includes('demo / All environments'))
  assert.ok(html.includes('10.20.0.0/24'))
  for (const value of Object.values(credentials)) assert.equal(html.includes(value), false)
})

test('provider credential replacement is explicit and provider changes cannot forward stale credentials', () => {
  const preserved = prepareProviderInput(configuration, '', credentials, false, 4)
  assert.equal('credentials' in preserved, false)
  assert.equal(preserved.expected_revision, 4)
  const infisical = prepareProviderInput(
    {
      ...configuration,
      kind: 'infisical',
      project_id: 'project-id',
      environment: 'prod',
      namespace: '../unfinished-vault-namespace',
    },
    '',
    credentials,
    true,
    0,
  )
  assert.deepEqual(infisical.credentials, {
    client_id: credentials.client_id,
    client_secret: credentials.client_secret,
  })
  assert.equal('mount' in infisical, false)
  assert.equal('namespace' in infisical, false)
  assert.equal(credentials.token, 'private-vault-token', 'draft stays intact until save succeeds')
  assert.throws(() => prepareProviderInput(configuration, '', credentials, false, 0), /required/)
})

test('access editor keeps removed project and environment grants visible for review', () => {
  const scopes = [
    { project: 'demo', environments: ['retired'] },
    { project: 'removed-project', environments: ['production'] },
  ]
  const html = renderToStaticMarkup(
    <SecretProviderScopes
      projects={[{ id: 'demo', name: 'demo', environments: [{ name: 'development' }] }]}
      scopes={scopes}
      onChange={() => {}}
    />,
  )
  assert.match(html, /aria-label="demo \/ retired"[^>]*checked=""/)
  assert.match(html, /aria-label="removed-project \/ production"[^>]*checked=""/)
  assert.match(html, /No longer available/)
  assert.equal(html.includes('All environments, including new ones'), false)
  assert.deepEqual(scopes[0].environments, ['retired'])
})

test('provider review rejects empty grants and unsafe source syntax without changing the draft', () => {
  assert.throws(
    () => prepareProviderInput({ ...configuration, scopes: [] }, '', credentials, true, 0),
    /Select between/,
  )
  assert.throws(
    () =>
      prepareProviderInput({ ...configuration, root_path: '../other' }, '', credentials, true, 0),
    /relative/,
  )
  assert.throws(
    () =>
      prepareProviderInput(
        { ...configuration, endpoint: 'https://user:password@example.test' },
        '',
        credentials,
        true,
        0,
      ),
    /without credentials/,
  )
  assert.equal(
    scopeSummary({ project: 'demo', environments: ['development'] }),
    'demo / development',
  )
  assert.equal(scopeSummary({ project: 'demo', environments: [] }), 'demo / All environments')
})

test('stale provider drafts can compare access but cannot rebase onto a replacement source', () => {
  const input = prepareProviderInput(configuration, '', credentials, false, 1)
  const current: SecretProvider = {
    ...configuration,
    root_path: '',
    revision: 2,
    created_at: '',
    updated_at: '',
  }
  assert.equal(sameProviderSource(input, current), true)
  assert.equal(
    sameProviderSource(input, { ...current, endpoint: 'https://different.example.test' }),
    false,
  )
  assert.equal(sameProviderSource(input, { ...current, private_cidrs: ['10.0.0.0/8'] }), false)
  assert.deepEqual(parentNavigation('/settings/secret-providers/vault/edit'), {
    to: '/settings/secret-providers',
    label: 'Back to providers',
  })
  assert.equal(parentNavigation('/settings/secret-providers')?.search?.tab, 'secret-providers')
})
