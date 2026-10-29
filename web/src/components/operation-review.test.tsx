import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { ReviewFacts, ReviewNotice } from './operation-review'
import { deleteServiceAction } from './delete-service-dialog'

test('review facts list scope and consequences and skip absent rows', () => {
  const html = renderToStaticMarkup(
    <ReviewFacts
      facts={[['Scope', 'demo / production'], false, ['Data', 'Volumes are kept: api-data.']]}
    />,
  )
  assert.match(html, /<dt>Scope<\/dt><dd>demo \/ production<\/dd>/)
  assert.match(html, /<dt>Data<\/dt><dd>Volumes are kept: api-data.<\/dd>/)
  assert.equal(html.match(/<dt>/g)?.length, 2)
})

test('review warnings stay visible text rather than tooltips', () => {
  const html = renderToStaticMarkup(
    <ReviewNotice tone="danger" title="Volume data cannot be recovered">
      Storage quota stays reserved.
    </ReviewNotice>,
  )
  assert.match(html, /review-notice-danger/)
  assert.match(html, /Volume data cannot be recovered/)
  assert.match(html, /Storage quota stays reserved/)
})

test('delete service action names the service and the volumes it erases', () => {
  assert.equal(deleteServiceAction('api', -1), 'Review deletion')
  assert.equal(deleteServiceAction('api', 0), 'Delete api')
  assert.equal(deleteServiceAction('api', 1), 'Delete api and 1 volume')
  assert.equal(deleteServiceAction('api', 2), 'Delete api and 2 volumes')
})
