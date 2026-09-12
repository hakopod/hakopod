import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { TOMLCode } from './toml-code'
import { specToTOML } from '../lib/toml'
import {
  MAX_TOML_HIGHLIGHT_CHARS,
  MAX_TOML_HIGHLIGHT_DEPTH,
  MAX_TOML_HIGHLIGHT_TOKENS,
  tokenizeTOML,
} from '../lib/toml-highlight'

function renderedText(html: string) {
  const entities: Record<string, string> = {
    '&amp;': '&',
    '&lt;': '<',
    '&gt;': '>',
    '&quot;': '"',
    '&#x27;': "'",
  }
  return html
    .replace(/<[^>]*>/g, '')
    .replace(/&(?:amp|lt|gt|quot|#x27);/g, (entity) => entities[entity])
}

test('TOML rendering keeps canonical text exact and escapes markup in quoted values', () => {
  const code = specToTOML({
    schema_version: 1,
    name: 'example <script>alert("&lt;")</script> # still a string',
    domains: { 'app.example.com': 'web' },
    services: {
      web: {
        image: 'example/web:1',
        command: ['echo', 'quoted "value"'],
        port: 8080,
        public: true,
      },
    },
  })
  const html = renderToStaticMarkup(<TOMLCode code={code} />)
  assert.equal(renderedText(html), code)
  assert.equal(html.includes('<script>'), false)
  assert.match(html, /class="toml-table"/)
  assert.match(html, /class="toml-key"/)
  assert.match(html, /class="toml-number"/)
  assert.match(html, /class="toml-boolean"/)
  assert.equal(html.includes('class="toml-comment"'), false)
})

test('TOML string boundaries protect embedded comment, bracket, and assignment characters', () => {
  const code = [
    '[services."web.#]name"] # table comment',
    '"key.#=" = "escaped \\" quote # [false]" # value comment',
    "literal = 'a # [ ] = true'",
    'list = [true, -12.5e+2, { "nested key" = "# string", enabled = false }]',
    'message = """first # line',
    'second [true] line"""',
    '',
  ].join('\n')
  const tokens = tokenizeTOML(code)
  const values = (kind: string) =>
    tokens.filter((token) => token.kind === kind).map((token) => code.slice(token.start, token.end))
  assert.deepEqual(values('table'), ['[services."web.#]name"]'])
  assert.deepEqual(values('comment'), ['# table comment', '# value comment'])
  assert.deepEqual(values('boolean'), ['true', 'false'])
  assert.deepEqual(values('number'), ['-12.5e+2'])
  assert.ok(values('key').includes('"nested key"'))
  assert.ok(values('string').includes('"""first # line\nsecond [true] line"""'))
  assert.equal(renderedText(renderToStaticMarkup(<TOMLCode code={code} />)), code)
})

test('oversized and deeply nested TOML retains its full remainder with bounded highlighting', () => {
  const inputs = [
    'value = "' + 'x'.repeat(MAX_TOML_HIGHLIGHT_CHARS + 100) + '"\nlast = true\n',
    'value = 1\n'.repeat(MAX_TOML_HIGHLIGHT_TOKENS + 10),
    'value = ' +
      '['.repeat(MAX_TOML_HIGHLIGHT_DEPTH + 10) +
      'true' +
      ']'.repeat(MAX_TOML_HIGHLIGHT_DEPTH + 10) +
      '\n',
  ]
  for (const code of inputs) {
    const tokens = tokenizeTOML(code)
    assert.ok(tokens.length <= MAX_TOML_HIGHLIGHT_TOKENS)
    assert.ok(tokens.every((token) => token.end <= MAX_TOML_HIGHLIGHT_CHARS))
    assert.ok((tokens.at(-1)?.end || 0) < code.length)
    const html = renderToStaticMarkup(<TOMLCode code={code} />)
    assert.ok((html.match(/<span\b/g) || []).length <= MAX_TOML_HIGHLIGHT_TOKENS)
    assert.equal(renderedText(html), code)
  }
})
