import assert from 'node:assert/strict'
import test from 'node:test'
import {
  findTLSIssuer,
  preferredTLSIssuer,
  preferredTLSMethod,
  tlsIssuerKey,
  tlsIssuersQueryKey,
  type TLSIssuer,
} from './tls-issuers.ts'

const shared: TLSIssuer = {
  name: 'letsencrypt',
  kind: 'ClusterIssuer',
  default: true,
  email: '',
  server: 'https://acme-v02.api.letsencrypt.org/directory',
  ready: true,
  conditions: [],
}
const application: TLSIssuer = { ...shared, kind: 'Issuer', default: false, ready: false }
const items = [application, shared]

test('application and installation issuer caches cannot share a scope', () => {
  assert.deepEqual(tlsIssuersQueryKey(), ['tls-issuers'])
  assert.deepEqual(tlsIssuersQueryKey('app-a'), ['tls-issuers', 'application', 'app-a'])
  assert.notDeepEqual(tlsIssuersQueryKey('app-a'), tlsIssuersQueryKey('app-b'))
  assert.notDeepEqual(tlsIssuersQueryKey(''), tlsIssuersQueryKey())
})

test('same-name default and application issuers resolve to distinct API references', () => {
  assert.notEqual(tlsIssuerKey(shared), tlsIssuerKey(application))
  assert.equal(findTLSIssuer(items, tlsIssuerKey(shared)), shared)
  assert.equal(findTLSIssuer(items, tlsIssuerKey(application)), application)
  assert.equal(findTLSIssuer(items, 'letsencrypt'), undefined)
  assert.equal(findTLSIssuer(items, 'Issuer:unavailable'), undefined)
  assert.equal(findTLSIssuer([shared], tlsIssuerKey(application)), undefined)
})

test('issuer defaults never replace current, user-selected, or explicitly cleared choices', () => {
  assert.equal(preferredTLSIssuer(items, undefined), tlsIssuerKey(shared))
  assert.equal(preferredTLSIssuer([application], undefined), '')
  assert.equal(preferredTLSIssuer([], undefined), '')
  assert.equal(preferredTLSIssuer(items, ''), '')
  assert.equal(preferredTLSIssuer(items, tlsIssuerKey(application)), tlsIssuerKey(application))
  assert.equal(
    preferredTLSIssuer(items, undefined, { issuer: 'letsencrypt', issuer_kind: 'Issuer' }),
    tlsIssuerKey(application),
  )
  assert.equal(
    preferredTLSIssuer(items, undefined, { issuer: 'letsencrypt' }),
    tlsIssuerKey(shared),
  )
  const missing = preferredTLSIssuer(items, undefined, { issuer: 'removed' })
  assert.equal(missing, 'ClusterIssuer:removed')
  assert.equal(findTLSIssuer(items, missing), undefined)
  assert.equal(preferredTLSIssuer([], tlsIssuerKey(application)), tlsIssuerKey(application))
})

test('managed Cloud defaults preserve uploaded certificates and explicit method choices', () => {
  assert.equal(preferredTLSMethod(undefined, undefined, true), 'issuer')
  assert.equal(preferredTLSMethod(undefined, undefined, false), 'upload')
  assert.equal(preferredTLSMethod(undefined, { certificate: 'uploaded-chain' }, true), 'upload')
  assert.equal(preferredTLSMethod(undefined, { issuer: 'existing' }, false), 'issuer')
  assert.equal(preferredTLSMethod('upload', { issuer: 'existing' }, true), 'upload')
  assert.equal(preferredTLSMethod('issuer', { certificate: 'uploaded-chain' }, true), 'issuer')
})
