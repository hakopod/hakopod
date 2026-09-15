import { test } from 'node:test'
import assert from 'node:assert/strict'
import { importDotenv, MAX_ENV_FILE_BYTES } from './dotenv'

test('dotenv imports quoted, multiline, export, empty and literal references without evaluation', () => {
  const rows = importDotenv(
    '\uFEFF# comment\r\nexport MODE=production\r\nEMPTY=\nURL="https://example.test/?a=b#hash" # comment\nTEXT="hello\\nworld"\nMULTI=\'one\ntwo\'\nLITERAL=${HOME}$(whoami)',
    [],
  )
  assert.deepEqual(Object.fromEntries(rows.map(({ name, value }) => [name, value])), {
    MODE: 'production',
    EMPTY: '',
    URL: 'https://example.test/?a=b#hash',
    TEXT: 'hello\nworld',
    MULTI: 'one\ntwo',
    LITERAL: '${HOME}$(whoami)',
  })
})

test('dotenv failure is atomic and does not echo sensitive values', () => {
  const existing = [{ id: 'old', name: 'MODE', value: 'old' }]
  for (const source of [
    'MODE=new',
    'A=1\nA=2',
    'BAD-NAME=private-value',
    'API_TOKEN=private-value',
    'DATABASE_URL=postgres://user:private-value@db/app',
    'A="private-value',
    'A="ok" private-value',
  ]) {
    assert.throws(
      () => importDotenv(source, existing),
      (error: Error) => !error.message.includes('private-value'),
    )
    assert.deepEqual(existing, [{ id: 'old', name: 'MODE', value: 'old' }])
  }
})

test('dotenv bounds and existing variable validation apply before import', () => {
  assert.throws(() => importDotenv('A=' + 'x'.repeat(MAX_ENV_FILE_BYTES), []), /512 KiB/)
  assert.throws(() => importDotenv('A=' + 'x'.repeat(4097), []), /4,096 bytes/)
  assert.throws(() => importDotenv('A=\0', []), /null/)
  assert.throws(() => importDotenv('# empty', []), /no environment/)
  assert.throws(
    () => importDotenv(Array.from({ length: 129 }, (_, i) => `A${i}=x`).join('\n'), []),
    /128/,
  )
})
