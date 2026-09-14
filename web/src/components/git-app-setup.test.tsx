import test from 'node:test'
import assert from 'node:assert/strict'
import { gitAppDestination } from './git-app-setup'
import type { components } from '../lib/api.generated'

test('GitHub setup accepts only fixed provider destinations with state', () => {
  const base: components['schemas']['GitAppSetup'] = {
    connection_id: 'fixture',
    phase: 'manifest',
    action_url: 'https://github.com/settings/apps/new?state=random',
    expires_at: '2026-01-01T00:00:00Z',
  }
  assert.equal(gitAppDestination(base), base.action_url)
  assert.equal(
    gitAppDestination({
      ...base,
      action_url: 'https://github.com/organizations/team/settings/apps/new?state=random',
    }),
    'https://github.com/organizations/team/settings/apps/new?state=random',
  )
  assert.equal(
    gitAppDestination({
      ...base,
      phase: 'install',
      action_url: 'https://github.com/apps/hakopod-team/installations/new?state=random',
    }),
    'https://github.com/apps/hakopod-team/installations/new?state=random',
  )
  for (const action_url of [
    'https://attacker.test/settings/apps/new?state=x',
    'https://github.com@attacker.test/settings/apps/new?state=x',
    'http://github.com/settings/apps/new?state=x',
    'https://github.com/settings/apps/new',
    'https://github.com/login?state=x',
    'https://github.com/apps/test/installations/new?state=x',
  ])
    assert.throws(() => gitAppDestination({ ...base, action_url }))
})
