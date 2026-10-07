import assert from 'node:assert/strict'
import test from 'node:test'
import { buildRunNeedsObservation } from './build-runs'

const imageReady = { status: 'completed', conclusion: 'success', image: 'example.invalid/app@sha256:fixture' }

test('automatic deployment remains observable after its build image is verified', () => {
  for (const auto_status of ['queued', 'processing'])
    assert.equal(buildRunNeedsObservation({ ...imageReady, automatic: true, auto_status }), true)
  for (const auto_status of ['ready', 'deployed', 'blocked', 'finished', 'superseded']) {
    assert.equal(buildRunNeedsObservation({ ...imageReady, automatic: true, auto_status }), false)
    assert.equal(buildRunNeedsObservation({ status: 'queued', automatic: true, auto_status }), false)
  }
})

test('manual completion stops polling while known build work remains observable', () => {
  assert.equal(buildRunNeedsObservation({ ...imageReady, automatic: false, auto_status: 'queued' }), false)
  assert.equal(buildRunNeedsObservation({ status: 'completed', conclusion: 'success', image: '' }), true)
  for (const status of ['dispatching', 'dispatch_unknown', 'queued', 'in_progress', 'cancelling'])
    assert.equal(buildRunNeedsObservation({ status }), true)
  for (const status of ['failed', 'cancelled'])
    assert.equal(buildRunNeedsObservation({ status }), false)
  for (const conclusion of ['failure', 'cancelled', 'timed_out'])
    assert.equal(buildRunNeedsObservation({ status: 'completed', conclusion }), false)
})

test('missing or future status fields do not create endless observation requests', () => {
  for (const run of [
    undefined,
    {},
    { status: 'future_state' },
    { status: 'completed' },
    { status: 'completed', conclusion: 'success' },
  ])
    assert.equal(buildRunNeedsObservation(run), false)
  for (const auto_status of [undefined, '', 'future_state'])
    assert.equal(buildRunNeedsObservation({ ...imageReady, automatic: true, auto_status }), false)
})
