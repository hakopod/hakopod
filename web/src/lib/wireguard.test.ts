import assert from 'node:assert/strict'
import { test } from 'node:test'
import { wireguardKeyPair, wireguardProfile } from './wireguard.ts'

test('generates a WireGuard key pair whose public key matches its private key', async () => {
  const pair = await wireguardKeyPair()
  const raw = Uint8Array.from(atob(pair.privateKey), (c) => c.charCodeAt(0))
  assert.equal(raw.length, 32)
  assert.equal(Uint8Array.from(atob(pair.publicKey), (c) => c.charCodeAt(0)).length, 32)
  // Re-import the private scalar and derive its public key independently.
  const prefix = Uint8Array.from([
    0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x6e, 0x04, 0x22, 0x04, 0x20,
  ])
  const key = await crypto.subtle.importKey(
    'pkcs8',
    new Uint8Array([...prefix, ...raw]),
    { name: 'X25519' },
    true,
    ['deriveBits'],
  )
  const jwk = await crypto.subtle.exportKey('jwk', key)
  assert.equal(Buffer.from(jwk.x!, 'base64url').toString('base64'), pair.publicKey)
})

test('writes a profile that routes only the VPN network', () => {
  const text = wireguardProfile({
    privateKey: 'cHJpdg==',
    address: '10.100.0.2',
    serverPublicKey: 'c2VydmVy',
    endpoint: 'vpn.example.com:51820',
    network: '10.100.0.0/24',
  })
  assert.match(text, /^Address = 10\.100\.0\.2\/32$/m)
  assert.match(text, /^AllowedIPs = 10\.100\.0\.0\/24$/m)
  assert.doesNotMatch(text, /0\.0\.0\.0\/0/)
})
