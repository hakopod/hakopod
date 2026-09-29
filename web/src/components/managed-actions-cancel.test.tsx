import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import type { Application } from '../lib/types'
import {
  nativeCancellationBlocked,
  nativeCancellationTarget,
  requestNativeJobCancellation,
} from '../lib/actions-cancellation'
import { NativeJobCancellationReview } from './managed-actions-cancel'
import { nativeJob, renderedButton, renderJob } from './managed-actions-workflows.test'

function activeJob() {
  const item = nativeJob()
  return {
    ...item,
    can_cancel: true,
    native_job: { ...item.native_job!, status: 'in_progress', conclusion: '' },
  }
}

const application = {
  id: 'fixture-application',
  name: 'Fixture runners',
  project: 'saved-project',
  environment: 'staging',
} as Application

test('cancellation controls require server eligibility and current loaded-project permission', () => {
  const active = activeJob()
  assert.ok(renderedButton(renderJob(active), 'Cancel job'))
  assert.equal(renderedButton(renderJob(active, false), 'Cancel job'), undefined)
  for (const item of [
    nativeJob(),
    { ...active, can_cancel: false },
    { ...active, can_cancel: undefined },
    { ...active, provider: 'github' as const },
    { ...active, native_job: undefined },
  ]) {
    assert.equal(nativeCancellationTarget(application, 'runner', item), null)
    assert.equal(renderedButton(renderJob(item), 'Cancel job'), undefined)
  }
})

test('cancellation request keeps the reviewed scope and refuses revoked or changed targets', async () => {
  const target = nativeCancellationTarget(application, 'saved-service', activeJob())!
  const signal = new AbortController().signal
  let calls = 0
  const send = async (actual: typeof target, actualSignal: AbortSignal) => {
    calls++
    assert.deepEqual(actual, target)
    assert.equal(actualSignal, signal)
    return { status: 'cancellation_requested', scope: 'job' as const }
  }
  await requestNativeJobCancellation(target, { ...target }, true, signal, send)
  assert.equal(calls, 1)
  for (const current of [
    null,
    { ...target, applicationID: 'other-app' },
    { ...target, service: 'other-service' },
    { ...target, jobID: 'different-job' },
    { ...target, runnerID: 'different-runner' },
    { ...target, environment: 'production' },
  ]) {
    await assert.rejects(requestNativeJobCancellation(target, current, true, signal, send))
  }
  await assert.rejects(
    requestNativeJobCancellation(target, target, false, signal, send),
    /permission/,
  )
  assert.equal(calls, 1, 'blocked reviews never dispatch a cancellation')
  await assert.rejects(
    requestNativeJobCancellation(target, target, true, signal, async () => ({
      status: 'unknown',
      scope: 'job',
    })),
    /not confirmed/,
  )
})

test('cancellation review retains original identifiers and a visible error while confirm is blocked', () => {
  const target = nativeCancellationTarget(application, 'saved-service', activeJob())!
  const error = 'GitLab has not confirmed job cancellation. Refresh the job before retrying.'
  const html = renderToStaticMarkup(
    <NativeJobCancellationReview
      target={target}
      blocked={nativeCancellationBlocked(target, target, false)}
      error={error}
      busy={false}
      onClose={() => {}}
      onConfirm={() => {}}
    />,
  )
  assert.match(html, /saved-project \/ staging/)
  assert.match(html, /^<div class="dialog-body /)
  assert.match(html, /saved-service/)
  assert.match(html, /9007199254740999/)
  assert.match(html, /role="alert"/)
  assert.ok(html.includes(error))
  assert.match(html, /no longer have permission/)
  const confirm = renderedButton(html, 'Cancel job')
  assert.ok(confirm)
  assert.match(confirm, /^<button\b[^>]*\sdisabled=""/)
})
