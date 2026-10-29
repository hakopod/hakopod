import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  RouterContextProvider,
  createRootRoute,
  createRouter,
} from '@tanstack/react-router'
import { canAccess, ScopeContext } from '../lib/scope'
import { importDotenv } from '../lib/dotenv'
import type { Identity } from '../lib/types'
import type { EnvironmentRow } from '../lib/service-environment'
import { DeploymentForm } from './deploy-dialog'
import { EnvironmentFields } from './runtime-settings-fields'
import { environmentIssues, formatDotenv, nameFromImage, serviceIssues } from './create-flow'

const row = (name: string, value: string, secret?: boolean): EnvironmentRow => ({
  id: `${name}-${value}-${secret ? 's' : 'p'}`,
  name,
  value,
  ...(secret ? { secret } : {}),
})

test('variable issues flag duplicates, bad names and empty secrets, and ignore blank rows', () => {
  const rows = [
    row('APP_ENV', 'production'),
    row('APP_ENV', 'staging'),
    row('9LIVES', 'x'),
    row('API_TOKEN', '', true),
    row('', ''),
    row('EMPTY_OK', ''),
  ]
  const issues = environmentIssues(rows)
  assert.equal(issues.size, 4)
  assert.match(issues.get(rows[0].id)!, /more than once/)
  assert.match(issues.get(rows[2].id)!, /letters, numbers and underscores/)
  assert.match(issues.get(rows[3].id)!, /secret value/)
  assert.equal(issues.has(rows[4].id), false)
  assert.equal(issues.has(rows[5].id), false)
})

test('bulk editor text round-trips plain values and never contains secrets', () => {
  const plain = [
    row('PLAIN', 'simple-value'),
    row('SPACED', ' leading and # hash '),
    row('QUOTED', 'say "hi"\\now'),
    row('MULTI', 'line one\nline two'),
    row('EMPTY', ''),
  ]
  const text = formatDotenv([...plain, row('DB_PASSWORD', 'hunter2', true), row('API_TOKEN', 'x')])
  assert.doesNotMatch(text, /hunter2|DB_PASSWORD|API_TOKEN/)
  const parsed = importDotenv(text, [], true)
  assert.deepEqual(
    parsed.map(({ name, value }) => [name, value]),
    plain.map(({ name, value }) => [name, value]),
  )
})

test('service issues and suggested names come from the draft', () => {
  assert.deepEqual(serviceIssues({ image: '' }).source, ['Enter a container image.'])
  const found = serviceIssues(
    { image: 'nginx' },
    { command: '"unclosed', args: '', variables: [row('A', '1'), row('A', '2')] },
  )
  assert.equal(found.source.length, 1)
  assert.equal(found.variables, 2)
  assert.equal(found.total, 3)
  assert.equal(nameFromImage('ghcr.io/acme/Shop_API:1.2@sha256:abc'), 'shop-api')
  assert.equal(nameFromImage('registry:5000/9web'), 'web')
  assert.equal(nameFromImage(''), '')
})

test('environment rows keep secrets in their own write-only section', () => {
  const html = renderToStaticMarkup(
    <EnvironmentFields
      label="web"
      rows={[row('APP_ENV', 'production'), row('APP_ENV', 'x'), row('DB_PASSWORD', 'p', true)]}
      onChange={() => {}}
      showIssues
    />,
  )
  assert.match(html, /aria-label="web secrets"/)
  assert.match(html, /env-section-secret/)
  assert.match(html, /Write-only/)
  assert.match(html, /type="password"[^>]*aria-label="web secret 1 secret value"/)
  assert.match(html, /APP_ENV appears more than once/)
  assert.match(html, /Bulk edit/)
})

function renderNew(props: { chooseSource?: boolean; builds?: boolean }) {
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  if (props.builds !== undefined)
    cache.setQueryData(['git-connections'], {
      items: [{ id: 'fixture', enabled: true, capabilities: { builds: props.builds } }],
    })
  const identity: Identity = {
    id: 'fixture-owner',
    name: 'Fixture owner',
    admin: true,
    owner: true,
    credential_type: 'browser',
    permissions: ['admin'],
    project: '',
    environment: '',
  }
  const router = createRouter({
    routeTree: createRootRoute(),
    history: createMemoryHistory({ initialEntries: ['/applications/new'] }),
  })
  try {
    return renderToStaticMarkup(
      <QueryClientProvider client={cache}>
        <RouterContextProvider router={router}>
          <ScopeContext.Provider
            value={{
              project: 'demo',
              environment: 'development',
              identity,
              can: (permission) => canAccess(identity, 'demo', permission),
              syncScope: () => {},
            }}
          >
            <DeploymentForm chooseSource={props.chooseSource} onClose={() => {}} />
          </ScopeContext.Provider>
        </RouterContextProvider>
      </QueryClientProvider>,
    )
  } finally {
    cache.clear()
  }
}

test('new applications start with the source, recommending builds when a connection can run them', () => {
  const html = renderNew({ chooseSource: true, builds: true })
  assert.equal(html.match(/type="radio"/g)?.length, 3)
  assert.match(html, /Where does this application come from\?/)
  assert.match(html, /checked="" value="repository"/)
  assert.match(html, /Choose repository/)
  assert.doesNotMatch(html, /Container image<\/label>|Application name/)
  const without = renderNew({ chooseSource: true, builds: false })
  assert.match(without, /checked="" value="image"/)
  assert.match(without, /Configure services/)
})

test('the services step shows services before shared application settings', () => {
  const html = renderNew({})
  assert.match(html, /aria-current="step"[^>]*>.*Services/)
  assert.match(html, /Review 1 service/)
  assert.match(html, /Image needed/)
  assert.match(html, /1 issue/)
  assert.ok(html.indexOf('Public web service') < html.indexOf('Application name'))
  assert.match(html, /Private service/)
  assert.match(html, /Worker/)
})
