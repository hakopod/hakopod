import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ScopeContext } from '../lib/scope'
import { InstallationAccess } from './installation-access'
import {
  prepareLoginProvider,
  prepareSMTP,
  type LoginProviderSettings,
  type SMTPSettings,
} from '../lib/installation-settings'

const provider: LoginProviderSettings = {
  provider: 'oidc',
  enabled: true,
  client_id: 'client',
  secret_configured: true,
  issuer_url: 'https://id.example.test/realm',
  revision: 7,
  callback_url: 'https://console.example.test/api/v1/auth/oauth/oidc/callback',
  encryption_ready: true,
}
const smtp: SMTPSettings = {
  revision: 3,
  source: 'operator',
  enabled: true,
  host: 'smtp.example.test',
  port: 587,
  security: 'starttls',
  username: 'sender',
  from_email: 'sender@example.test',
  password_set: true,
  encryption_ready: true,
}

test('installation forms retain stored credentials unless replacement or removal is explicit', () => {
  assert.equal(
    'client_secret' in prepareLoginProvider(provider, 'keep', 'stale-local-secret'),
    false,
  )
  assert.equal('clear_secret' in prepareLoginProvider(provider, 'keep', ''), false)
  assert.equal(
    prepareLoginProvider(provider, 'replace', 'new-local-secret').client_secret,
    'new-local-secret',
  )
  assert.throws(() => prepareLoginProvider(provider, 'replace', ''), /new client secret/)
  assert.throws(() => prepareLoginProvider(provider, 'clear', ''), /enabled provider/)
  assert.equal(
    prepareLoginProvider({ ...provider, enabled: false }, 'clear', '').clear_secret,
    true,
  )
  assert.equal(prepareLoginProvider(provider, 'keep', '').expected_revision, 7)
  assert.equal('password' in prepareSMTP(smtp, 'keep', 'stale-local-password'), false)
  assert.equal('clear_password' in prepareSMTP(smtp, 'keep', ''), false)
  assert.equal(prepareSMTP(smtp, 'replace', 'new-local-password').password, 'new-local-password')
  assert.throws(() => prepareSMTP(smtp, 'clear', ''), /SMTP authentication/)
  assert.equal(prepareSMTP({ ...smtp, username: '' }, 'clear', '').clear_password, true)
  assert.equal(prepareSMTP(smtp, 'keep', '').expected_revision, 3)
})

test('installation form validation rejects unsafe issuer URLs and invalid ports', () => {
  for (const issuer_url of [
    'http://id.example.test',
    'https://user:password@id.example.test',
    'https://id.example.test/?token=value',
    'https://id.example.test/#token',
  ])
    assert.throws(
      () => prepareLoginProvider({ ...provider, issuer_url }, 'keep', ''),
      /HTTPS issuer/,
    )
  for (const port of [0, 65536, NaN, 12.5])
    assert.throws(() => prepareSMTP({ ...smtp, port }, 'keep', ''), /port/)
})

test('installation controls stay unmounted for Cloud and non-admin identities', () => {
  for (const admin of [false, true])
    for (const mode of ['managed-cloud', 'self-hosted', undefined]) {
      const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
      if (mode) cache.setQueryData(['auth-status'], { deployment_mode: mode })
      const identity = {
        id: 'fixture',
        name: 'Fixture',
        admin,
        owner: admin,
        permissions: [],
        project: '',
        environment: '',
        credential_type: 'browser' as const,
      }
      try {
        const html = renderToStaticMarkup(
          <QueryClientProvider client={cache}>
            <ScopeContext.Provider
              value={{
                project: '',
                environment: '',
                identity,
                can: () => false,
                syncScope: () => {},
              }}
            >
              <InstallationAccess>
                <button>Private installation control</button>
              </InstallationAccess>
            </ScopeContext.Provider>
          </QueryClientProvider>,
        )
        assert.equal(
          html.includes('Private installation control'),
          admin && mode === 'self-hosted',
          `${admin}/${mode}`,
        )
      } finally {
        cache.clear()
      }
    }
})

test('SMTP validation applies backend byte limits before review', () => {
  assert.throws(() => prepareSMTP({ ...smtp, username: 'é'.repeat(129) }, 'keep', ''), /256 bytes/)
  assert.throws(
    () => prepareSMTP({ ...smtp, from_email: 'é'.repeat(128) }, 'keep', ''),
    /254 bytes/,
  )
  assert.throws(() => prepareSMTP(smtp, 'replace', 'é'.repeat(2049)), /4096 bytes/)
  assert.equal(prepareSMTP(smtp, 'replace', 'é'.repeat(2048)).password?.length, 2048)
  for (const delimiter of ['\0', '\r', '\n'])
    assert.throws(
      () => prepareSMTP(smtp, 'replace', `password${delimiter}suffix`),
      /NUL or line breaks/,
    )
  assert.throws(
    () => prepareSMTP({ ...smtp, username: 'user\u0085name' }, 'keep', ''),
    /control characters/,
  )
})
