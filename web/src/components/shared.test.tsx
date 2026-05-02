import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { Copy } from './shared'

test('copying a generated key cannot submit its credential creation form', () => {
  const html = renderToStaticMarkup(
    <form method="post">
      <Copy value="test-value-not-a-credential" label="Copy key" />
    </form>,
  )
  const button = html.match(/<button\b[^>]*>/)?.[0]
  assert.ok(button, 'the generated-key control renders a real button')
  // A button without an explicit type submits its containing form by default.
  assert.match(button, /\btype="button"/, 'copy must have no form submission default action')
  assert.ok(html.includes('Copy key'))
  assert.equal(html.includes('test-value-not-a-credential'), false)
})
