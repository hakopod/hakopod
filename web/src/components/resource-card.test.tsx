import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { APIError } from '../lib/api'
import { CardMetric, ObservedState, ResourceCard, ResourceError } from './resource-card'

test('resource cards say what is absent instead of showing a blank or a zero', () => {
  const html = renderToStaticMarkup(
    <ResourceCard
      cue={null}
      name="api"
      meta="Worker"
      state={<ObservedState status="not observed" />}
      metric={<CardMetric label="Replicas" window="now" />}
    />,
  )
  assert.match(html, /No endpoint/)
  assert.match(html, /Not observed/)
  assert.match(html, /Replicas <span>· now<\/span>/)
  assert.match(html, /Unavailable/)
  assert.equal(/>0</.test(html), false)
  assert.equal(
    (html.match(/class="resource-card-(top|meta|endpoint|state|metric)"/g) || []).length,
    5,
  )
})

test('a permission failure is shown as denied, not as a generic error', () => {
  const html = renderToStaticMarkup(
    <ResourceError error={new APIError('forbidden', 403)} retry={() => {}} subject="builds" />,
  )
  assert.match(html, /You do not have access to builds/)
})
