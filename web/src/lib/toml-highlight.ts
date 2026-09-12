export type TOMLTokenKind =
  'table' | 'key' | 'string' | 'number' | 'boolean' | 'comment' | 'punctuation'

export type TOMLToken = { start: number; end: number; kind: TOMLTokenKind }

export const MAX_TOML_HIGHLIGHT_CHARS = 64 * 1024
export const MAX_TOML_HIGHLIGHT_TOKENS = 512
export const MAX_TOML_HIGHLIGHT_DEPTH = 32

const decimal = /^[+-]?(?:\d(?:_?\d)*(?:\.\d(?:_?\d)*)?(?:[eE][+-]?\d(?:_?\d)*)?|inf|nan)$/
const integerBase = /^0(?:x[\da-fA-F](?:_?[\da-fA-F])*|o[0-7](?:_?[0-7])*|b[01](?:_?[01])*)$/
const bareKey = /[A-Za-z0-9_-]/
const boundary = /[\s#[\]{},="']/
const whitespace = /\s/

function quotedEnd(source: string, start: number, limit: number): number {
  const quote = source[start]
  const triple = start + 2 < limit && source[start + 1] === quote && source[start + 2] === quote
  let end = start + (triple ? 3 : 1)
  while (end < limit) {
    if (quote === '"' && source[end] === '\\') {
      end += 2
    } else if (source[end] === quote) {
      if (!triple) return end + 1
      if (end + 2 < limit && source[end + 1] === quote && source[end + 2] === quote) return end + 3
      end++
    } else if (!triple && (source[end] === '\n' || source[end] === '\r')) {
      return -1
    } else {
      end++
    }
  }
  return -1
}

function tableEnd(source: string, start: number, limit: number): number {
  const array = start + 1 < limit && source[start + 1] === '['
  let end = start + (array ? 2 : 1)
  while (end < limit) {
    const char = source[end]
    if (char === '"' || char === "'") {
      end = quotedEnd(source, end, limit)
      if (end < 0) return -1
    } else if (char === ']') {
      return array ? (end + 1 < limit && source[end + 1] === ']' ? end + 2 : -1) : end + 1
    } else if (char === '\n' || char === '\r' || char === '#') {
      return -1
    } else {
      end++
    }
  }
  return -1
}

// Ranges reference the original text. Unscanned or unrecognized content stays plain;
// this is a bounded display lexer, never a TOML parser or validator.
export function tokenizeTOML(source: string): TOMLToken[] {
  const tokens: TOMLToken[] = []
  const containers: ('array' | 'table')[] = []
  const limit = Math.min(source.length, MAX_TOML_HIGHLIGHT_CHARS)
  let index = 0
  let lineStart = true
  let key = true
  while (index < limit && tokens.length < MAX_TOML_HIGHLIGHT_TOKENS) {
    const start = index
    const char = source[index]
    if (whitespace.test(char)) {
      if (char === '\n' || char === '\r') {
        lineStart = true
        if (!containers.length) key = true
      }
      index++
      continue
    }
    if (char === '#') {
      while (index < limit && source[index] !== '\n' && source[index] !== '\r') index++
      if (index === limit && limit < source.length) break
      tokens.push({ start, end: index, kind: 'comment' })
    } else if (char === '[' && lineStart && key && !containers.length) {
      index = tableEnd(source, start, limit)
      if (index < 0) break
      tokens.push({ start, end: index, kind: 'table' })
    } else if (char === '"' || char === "'") {
      index = quotedEnd(source, start, limit)
      if (index < 0) break
      tokens.push({ start, end: index, kind: key ? 'key' : 'string' })
    } else if ('=.,[]{}'.includes(char)) {
      if (char === '[' || char === '{') {
        if (containers.length === MAX_TOML_HIGHLIGHT_DEPTH) break
        containers.push(char === '[' ? 'array' : 'table')
        key = char === '{'
      } else if (char === ']' || char === '}') {
        if (containers.at(-1) === (char === ']' ? 'array' : 'table')) containers.pop()
        key = false
      } else if (char === ',') {
        key = containers.at(-1) === 'table'
      } else if (char === '=') {
        key = false
      }
      index++
      tokens.push({ start, end: index, kind: 'punctuation' })
    } else if (key && bareKey.test(char)) {
      while (index < limit && bareKey.test(source[index])) index++
      if (index === limit && limit < source.length && bareKey.test(source[index])) break
      tokens.push({ start, end: index, kind: 'key' })
    } else if (!key) {
      while (index < limit && !boundary.test(source[index])) index++
      if (index === limit && limit < source.length && !boundary.test(source[index])) break
      const value = source.slice(start, index)
      if (value === 'true' || value === 'false') tokens.push({ start, end: index, kind: 'boolean' })
      else if (decimal.test(value) || integerBase.test(value))
        tokens.push({ start, end: index, kind: 'number' })
    } else {
      index++
    }
    lineStart = false
  }
  return tokens
}
