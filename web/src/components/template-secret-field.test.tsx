import assert from 'node:assert/strict'
import test from 'node:test'
import { createPrivateKey } from 'node:crypto'
import { renderToStaticMarkup } from 'react-dom/server'
import { generateTemplateSecretValue, TemplateSecretField } from './template-secret-field'

test('generated template credentials satisfy the server format requirements', async () => {
  for (const format of ['password', 'token', 'token64', 'base64-32', 'hex32']) {
    const value = await generateTemplateSecretValue(format)
    if (format === 'hex32') {
      assert.match(value, /^[0-9a-f]{32}$/)
      assert.equal(Buffer.from(value, 'hex').length, 16)
    } else {
      const bytes = Buffer.from(value, 'base64')
      assert.equal(bytes.length, format === 'token64' ? 48 : 32)
      assert.equal(bytes.toString('base64'), value)
      if (format === 'token64') assert.equal(value.length, 64)
    }
  }
})

test('generated RSA credentials decode into a 2048-bit PKCS8 private key', async () => {
  const value = await generateTemplateSecretValue('base64-rsa-private-key')
  const pem = Buffer.from(value, 'base64')
  assert.ok(pem.toString('base64') === value, 'the private key has canonical Base64 encoding')
  assert.ok(pem.toString('ascii').startsWith('-----BEGIN PRIVATE KEY-----\n'))
  assert.ok(pem.toString('ascii').endsWith('\n-----END PRIVATE KEY-----\n'))
  const key = createPrivateKey({ key: pem, format: 'pem' })
  assert.equal(key.asymmetricKeyType, 'rsa')
  assert.equal(key.asymmetricKeyDetails?.modulusLength, 2048)
  assert.ok(value.length <= 4096)
})

test('RSA credential input accepts existing 4096-bit keys within a bounded field', () => {
  const html = renderToStaticMarkup(
    <TemplateSecretField
      templateId="fixture-only"
      field={{
        name: 'rsa',
        description: '',
        format: 'base64-rsa-private-key',
        generate: true,
        optional: false,
      }}
      query={{ project: 'fixture', environment: 'development', application: 'fixture' }}
      value=""
      onChange={() => {}}
      replacing={false}
      busy={false}
      onBusy={() => {}}
      onCancel={() => {}}
      onSaved={() => {}}
    />,
  )
  const limit = Number(html.match(/maxLength="(\d+)"/i)?.[1])
  assert.ok(limit >= 5 * 1024, 'the field accepts a Base64-wrapped 4096-bit private key')
  assert.ok(limit <= 8192, 'the field remains bounded')
})
