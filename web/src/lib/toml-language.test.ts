import { strict as assert } from 'node:assert'
import { test } from 'node:test'
import { tomlDiagnostics } from './toml-language'

test('TOML syntax errors never include source secret values', () => {
  const issues = tomlDiagnostics('token = "private-secret-value')
  assert.equal(issues.length, 1)
  assert.ok(!issues[0].message.includes('private-secret-value'))
})
test('schema guidance accepts service environment maps and detects unknown fields', () => {
  assert.deepEqual(tomlDiagnostics('schema_version = 1\nname = "demo"\n[services.web]\nimage = "nginx"\n[services.web.env]\nMODE = "dev"'), [])
  assert.match(tomlDiagnostics('schema_version = 1\nunknown = true')[0].message, /Unknown configuration field/)
})
