import test from 'node:test'
import assert from 'node:assert/strict'
import { formatProcessCommand, parseProcessCommand } from './process-command.ts'

test('runtime commands preserve quoted arguments and never expand shell expressions', () => {
  const words = [
    'uvicorn',
    'main:app',
    '--host',
    '0.0.0.0',
    '--port',
    '8000',
    '',
    "it's a value",
    '$PORT',
    '$(touch /tmp/never)',
    'a\\b',
  ]
  assert.deepEqual(parseProcessCommand(formatProcessCommand(words)), words)
  assert.deepEqual(parseProcessCommand('uvicorn "main:app" --host 0.0.0.0'), words.slice(0, 4))
  assert.deepEqual(parseProcessCommand('sh -c "exec uvicorn main:app --port $PORT"'), [
    'sh',
    '-c',
    'exec uvicorn main:app --port $PORT',
  ])
  assert.deepEqual(parseProcessCommand('  '), [])
})
test('unfinished runtime commands fail without dropping entered text', () => {
  for (const value of ['"unfinished', "'unfinished", 'unfinished' + String.fromCharCode(92)])
    assert.throws(() => parseProcessCommand(value), /Close quotes/)
})
