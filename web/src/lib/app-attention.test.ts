import assert from 'node:assert/strict'
import test from 'node:test'
import {
  attentionRank,
  healthSentence,
  issuePhrase,
  lastErrorLine,
  specChanges,
} from './app-attention.ts'

test('failing services sort first and are named in the health sentence', () => {
  assert.ok(attentionRank('failed') < attentionRank('blocked'))
  assert.ok(attentionRank('blocked') < attentionRank('ready'))
  const sentence = healthSentence(
    [
      { name: 'api', status: 'ready' },
      { name: 'reports', status: 'blocked', message: 'CrashLoopBackOff: back-off' },
      { name: 'queue', status: 'ready' },
    ],
    { revision: 2, status: 'failed' },
  )
  assert.deepEqual(sentence, [
    '1 of 3 services failing',
    'reports crash-looping',
    'revision 2 failed',
  ])
  assert.deepEqual(healthSentence([{ name: 'api', status: 'ready' }]), ['The service is running'])
  assert.deepEqual(healthSentence([{ name: 'a', status: 'not observed' }]), [
    'Health not observed yet',
  ])
  assert.equal(issuePhrase('blocked', 'ImagePullBackOff'), 'unable to pull its image')
})

test('revision changes are summarised in plain words', () => {
  const before = {
    schema_version: 1,
    name: 'billing',
    services: {
      api: { image: 'docker.io/library/python:3.13.14@sha256:a', replicas: 1, env: { A: '1' } },
      old: { image: 'x' },
    },
  }
  const after = {
    schema_version: 1,
    name: 'billing',
    services: {
      api: {
        image: 'docker.io/library/python:3.13.15@sha256:b',
        replicas: 2,
        env: { A: '2', B: '1' },
      },
      reports: { image: 'x' },
    },
  }
  assert.deepEqual(specChanges(before as never, after as never), [
    '+reports',
    '−old',
    'api image python:3.13.14 → python:3.13.15',
    'api replicas 1 → 2',
    'api variables +1 ~1',
  ])
  assert.deepEqual(specChanges(undefined, after as never), ['First revision · 2 services'])
  assert.deepEqual(specChanges(after as never, after as never), ['No configuration change'])
})

test('the log line shown is the last error, not the last line', () => {
  assert.equal(
    lastErrorLine('starting\nError: DATABASE_URL is not set\nshutting down\n'),
    'Error: DATABASE_URL is not set',
  )
  assert.equal(lastErrorLine('ready on :8080\n'), 'ready on :8080')
  assert.equal(lastErrorLine(''), '')
  assert.equal(
    lastErrorLine('2026-10-29T16:07:30.000000000Z Error: DATABASE_URL is not set'),
    'Error: DATABASE_URL is not set',
  )
  assert.equal(lastErrorLine('Error: boom\n[incomplete log line withheld]'), 'Error: boom')
})
