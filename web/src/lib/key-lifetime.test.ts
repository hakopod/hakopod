import assert from 'node:assert/strict'
import test from 'node:test'
import { keyLifetimeInput, keyLifetimeOptions } from './key-lifetime'

test('Cloud no-expiry selection sends explicit consent without an expiry date', () => {
  assert.deepEqual(keyLifetimeInput('never', true), { never_expires: true })
  assert.equal(keyLifetimeOptions(true).at(-1)?.label, 'Never expires')
})

test('self-hosted keys cannot select or submit an unlimited lifetime', () => {
  assert.equal(
    keyLifetimeOptions(false).some((option) => option.value === 'never'),
    false,
  )
  assert.throws(() => keyLifetimeInput('never', false), /supported key lifetime/)
})

test('dated keys retain a bounded expiry and invalid selections fail before submission', () => {
  const now = Date.parse('2026-10-01T00:00:00Z')
  assert.deepEqual(keyLifetimeInput('30', true, now), { expires_at: '2026-10-31T00:00:00.000Z' })
  for (const value of ['', '0', '-1', 'Infinity', '365']) {
    assert.throws(() => keyLifetimeInput(value, true, now), /supported key lifetime/)
  }
})
