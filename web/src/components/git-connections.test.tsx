import test from 'node:test'
import assert from 'node:assert/strict'
import { gitConnectionOptions, type GitConnection } from '../lib/git-connections'

const connection = (id: string, provider: 'github' | 'gitlab', builds = true): GitConnection => ({
  id,
  name: id,
  provider,
  auth_kind: 'token',
  revision: 1,
  enabled: true,
  account: '',
  subject_id: 0,
  github_app_id: 0,
  installation_id: 0,
  legacy: false,
  configured: true,
  token_configured: true,
  private_repositories: true,
  webhook_path: `/api/v1/webhooks/git/${id}`,
  status: 'ready',
  capabilities: { read_source: true, builds },
  updated_at: '',
})
test('Git selection requires an accessible connection and never substitutes a missing default', () => {
  const options = gitConnectionOptions(
    [connection('named-github', 'github'), connection('named-gitlab', 'gitlab')],
    'github',
    '',
    false,
  )
  assert.equal(options[0].value, '')
  assert.equal(options[0].label, 'Choose a connection')
  assert.equal(options[0].disabled, true)
  assert.deepEqual(
    options.map((item) => item.value),
    ['', 'named-github'],
  )
  const unavailable = gitConnectionOptions([], 'github', 'removed-connection', false)
  assert.equal(unavailable.find((item) => item.value === 'removed-connection')?.disabled, true)
})
test('Git selection disables unavailable builds while preserving current connection identity', () => {
  const options = gitConnectionOptions(
    [
      connection('gitlab-default', 'gitlab'),
      connection('read-only', 'gitlab', false),
      { ...connection('disabled', 'gitlab'), enabled: false },
    ],
    'gitlab',
    'read-only',
    true,
  )
  assert.equal(options.find((item) => item.value === '')?.label, 'gitlab-default')
  assert.equal(options.find((item) => item.value === 'read-only')?.disabled, true)
  assert.equal(options.find((item) => item.value === 'disabled')?.disabled, true)
})

test('legacy default IDs remain selectable in migrated source and build bindings', () => {
  for (const provider of ['github', 'gitlab'] as const) {
    const options = gitConnectionOptions(
      [connection(`${provider}-default`, provider)],
      provider,
      `${provider}-default`,
      true,
    )
    assert.equal(options.length, 1)
    assert.equal(options[0].value, '')
    assert.equal(options[0].disabled, false)
  }
})

test('source selectors preserve but disable connections requiring authorization', () => {
  const item = {
    ...connection('needs-authorization', 'gitlab'),
    auth_kind: 'gitlab_oauth' as const,
    status: 'reauthorize',
    capabilities: { read_source: false, builds: false },
  }
  const options = gitConnectionOptions(
    [item, connection('gitlab-default', 'gitlab')],
    'gitlab',
    item.id,
    false,
  )
  assert.equal(options.find((option) => option.value === item.id)?.disabled, true)
  assert.match(
    options.find((option) => option.value === item.id)?.label || '',
    /Source access unavailable/,
  )
  assert.equal(options.find((option) => option.value === '')?.disabled, false)
})
