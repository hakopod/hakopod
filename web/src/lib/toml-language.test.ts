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

test('managed binding connection options have editor guidance without false unknown-field errors', () => {
  assert.deepEqual(tomlDiagnostics(`schema_version = 1
name = "reports"
[services.web]
image = "example.test/reports@sha256:${'a'.repeat(64)}"
[services.web.bindings.DATABASE_URL]
protocol = "postgres"
managed_database = "${'b'.repeat(32)}"
endpoint = "read_write"
username = "reporter"
database = "analytics"
password = { ref = "reporter-password" }
ssl_mode = "verify-full"
`), [])
})

test('canonical template TOML accepts empty daemon and external-binding fields', () => {
  assert.deepEqual(tomlDiagnostics(`schema_version = 1
name = "outpost-fixture"
[services.broker]
image = "example.test/broker@sha256:${'a'.repeat(64)}"
container_daemon = ""
[services.delivery]
image = "example.test/delivery@sha256:${'b'.repeat(64)}"
container_daemon = ""
[services.delivery.bindings.POSTGRES_URL]
service = "db"
protocol = "postgres"
external_database = ""
external_database_revision = 0
ssl_mode = ""
[services.delivery.bindings.RABBITMQ_SERVER_URL]
service = "broker"
protocol = "amqp"
external_database = ""
external_database_revision = 0
ssl_mode = ""
`), [])
})

test('external database references and approved daemon names remain recognized in TOML', () => {
  assert.deepEqual(tomlDiagnostics(`schema_version = 1
name = "reports"
[services.web]
image = "example.test/reports@sha256:${'a'.repeat(64)}"
container_daemon = "runner-daemon"
[services.web.bindings.DATABASE_URL]
protocol = "postgres"
external_database = "${'b'.repeat(32)}"
external_database_revision = 2
`), [])
})
