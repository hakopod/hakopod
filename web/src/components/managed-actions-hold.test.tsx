import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import {
  holdReleaseBlocked,
  requestHoldRelease,
  type ActionsHoldState,
  type HoldReview,
} from '../lib/actions-hold'
import { ProviderHoldReview } from './managed-actions-hold'
import { nativeJob, renderedButton } from './managed-actions-workflows.test'

function reviewFixture(): HoldReview {
  const first = nativeJob().native_job!
  const second = {
    ...first,
    identity: { ...first.identity, job_id: '9007199254741001' },
    name: 'Unexpected second job',
  }
  return {
    applicationID: 'fixture-application',
    applicationName: 'Fixture runners',
    project: 'saved-project',
    environment: 'staging',
    service: 'saved-runner',
    hold: {
      id: '22222222-2222-4222-8222-222222222222',
      reason: 'runner_reuse',
      provider: 'gitlab',
      instance_url: 'https://gitlab.example.test/ci',
      slot_id: 'fixture-slot',
      observed_at: '2026-09-29T06:46:00Z',
      jobs: [first, second],
    },
  }
}

test('hold release requires current evidence, completed cleanup, permission and explicit acknowledgement', async () => {
  const review = reviewFixture()
  const ready: ActionsHoldState = { hold: review.hold, active_slots: 0 }
  const signal = new AbortController().signal
  let calls = 0
  const send = async (actual: HoldReview, actualSignal: AbortSignal) => {
    calls++
    assert.deepEqual(actual, review)
    assert.equal(actualSignal, signal)
    return { status: 'released' as const, hold_id: review.hold.id }
  }
  for (const state of [
    undefined,
    { ...ready, active_slots: 1 },
    { ...ready, hold: null },
    { ...ready, hold: { ...review.hold, id: '33333333-3333-4333-8333-333333333333' } },
  ]) {
    await assert.rejects(requestHoldRelease(review, state, true, true, signal, send))
  }
  await assert.rejects(requestHoldRelease(review, ready, false, true, signal, send), /permission/)
  await assert.rejects(requestHoldRelease(review, ready, true, false, signal, send), /Confirm/)
  assert.equal(calls, 0)
  assert.equal(holdReleaseBlocked(review, ready, true, true), '')
  await requestHoldRelease(review, ready, true, true, signal, send)
  assert.equal(calls, 1)
  await assert.rejects(
    requestHoldRelease(review, ready, true, true, signal, async () => ({
      status: 'released',
      hold_id: 'another-hold',
    })),
    /not confirmed/,
  )
})

test('hold review preserves both recorded job IDs, acknowledgement and failures without enabling premature release', () => {
  const review = reviewFixture()
  const html = renderToStaticMarkup(
    <ProviderHoldReview
      review={review}
      current={{ hold: review.hold, active_slots: 2 }}
      permitted
      acknowledged
      onAcknowledge={() => {}}
      busy={false}
      refreshing={false}
      error="Cleanup is still running."
      onRefresh={() => {}}
      onClose={() => {}}
      onRelease={() => {}}
    />,
  )
  for (const text of [
    'saved-project / staging',
    'https://gitlab.example.test/ci',
    '9007199254740999',
    '9007199254741001',
    'Cleanup is still running.',
    'allows the pool to start new runners',
  ])
    assert.ok(html.includes(text), text)
  assert.match(html, /type="checkbox"[^>]*checked=""/)
  assert.match(html, /^<div class="dialog-body /)
  assert.match(html, /role="alert"/)
  const release = renderedButton(html, 'Release hold')
  assert.ok(release)
  assert.match(release, /^<button\b[^>]*\sdisabled=""/)
  const viewer = renderToStaticMarkup(
    <ProviderHoldReview
      review={review}
      current={{ hold: review.hold, active_slots: 0 }}
      permitted={false}
      acknowledged={false}
      onAcknowledge={() => {}}
      busy={false}
      refreshing={false}
      error=""
      onRefresh={() => {}}
      onClose={() => {}}
      onRelease={() => {}}
    />,
  )
  assert.match(viewer, /Recorded jobs/)
  assert.doesNotMatch(viewer, /type="checkbox"/)
  assert.equal(renderedButton(viewer, 'Release hold'), undefined)
})
