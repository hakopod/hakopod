import { parse } from 'smol-toml'
import schemas from './editor-schema.json'
import type * as Monaco from 'monaco-editor'

type Schema = {
  type?: string
  properties?: Record<string, Schema>
  additionalProperties?: Schema | boolean
  items?: Schema
  enum?: (string | number)[]
  description?: string
  $ref?: string
}
const catalog = schemas as Record<string, Schema>
const resolve = (schema: Schema): Schema =>
  schema.$ref ? catalog[schema.$ref.split('/').pop()!] : schema
function sectionSchema(path: string[]) {
  let schema = catalog.Spec
  for (const key of path) {
    schema = resolve(schema)
    const next =
      schema.properties?.[key] ||
      (typeof schema.additionalProperties === 'object' ? schema.additionalProperties : undefined)
    if (!next) return undefined
    schema = resolve(next)
    if (schema.type === 'array' && schema.items) schema = resolve(schema.items)
  }
  return resolve(schema)
}
export function tomlDiagnostics(
  source: string,
): { message: string; line: number; column: number }[] {
  try {
    const parsed = parse(source)
    const issues: { message: string; line: number; column: number }[] = []
    const check = (value: unknown, raw: Schema, path: string) => {
      if (issues.length >= 16) return
      const schema = resolve(raw)
      if (schema.type === 'object' && value && typeof value === 'object' && !Array.isArray(value)) {
        for (const [key, child] of Object.entries(value)) {
          const field =
            schema.properties?.[key] ||
            (typeof schema.additionalProperties === 'object'
              ? schema.additionalProperties
              : undefined)
          if (field) check(child, field, path ? `${path}.${key}` : key)
          else if (schema.additionalProperties !== true && schema.properties)
            issues.push({
              message: `Unknown configuration field: ${path ? path + '.' : ''}${key}`,
              line: 1,
              column: 1,
            })
        }
      } else if (schema.type === 'array' && Array.isArray(value) && schema.items)
        value.forEach((child) => check(child, schema.items!, path))
      else if (
        schema.type &&
        (schema.type === 'integer'
          ? !Number.isInteger(value)
          : schema.type === 'array'
            ? !Array.isArray(value)
            : typeof value !== schema.type)
      )
        issues.push({ message: `${path} must be ${schema.type}.`, line: 1, column: 1 })
    }
    check(parsed, catalog.Spec, '')
    return issues
  } catch (error) {
    const failure = error as Error & { line?: number; column?: number }
    return [
      {
        message: 'Invalid TOML syntax. Check this position.',
        line: failure.line || 1,
        column: failure.column || 1,
      },
    ]
  }
}
let registered = false
export function registerTOML(monaco: typeof Monaco) {
  if (registered) return
  registered = true
  monaco.languages.register({ id: 'hakopod-toml', extensions: ['.toml'] })
  monaco.languages.setMonarchTokensProvider('hakopod-toml', {
    tokenizer: {
      root: [
        [/#.*$/, 'comment'],
        [/"""/, 'string', '@multiline'],
        [/"([^"\\]|\\.)*"/, 'string'],
        [/'[^']*'/, 'string'],
        [/\[\[?[^\]]+\]\]?/, 'type'],
        [/\b(true|false)\b/, 'keyword'],
        [/\b[+-]?\d[\d_.eE+-]*\b/, 'number'],
        [/[A-Za-z_][\w-]*(?=\s*=)/, 'key'],
        [/[=,{}\[\]]/, 'delimiter'],
      ],
      multiline: [
        [/"""/, 'string', '@pop'],
        [/./, 'string'],
      ],
    },
  })
  monaco.languages.setLanguageConfiguration('hakopod-toml', {
    comments: { lineComment: '#' },
    brackets: [
      ['[', ']'],
      ['{', '}'],
    ],
    autoClosingPairs: [
      { open: '"', close: '"' },
      { open: '[', close: ']' },
      { open: '{', close: '}' },
    ],
  })
  monaco.languages.registerCompletionItemProvider('hakopod-toml', {
    triggerCharacters: ['.', '=', '['],
    provideCompletionItems(model, position) {
      const before = model.getValueInRange({
        startLineNumber: 1,
        startColumn: 1,
        endLineNumber: position.lineNumber,
        endColumn: position.column,
      })
      const sections = [...before.matchAll(/^\s*\[\[?([^\]]+)\]\]?/gm)]
      const path =
        sections
          .at(-1)?.[1]
          .split('.')
          .map((key) => key.trim().replace(/^['"]|['"]$/g, '')) || []
      const schema = sectionSchema(path)
      const word = model.getWordUntilPosition(position)
      const range = {
        startLineNumber: position.lineNumber,
        endLineNumber: position.lineNumber,
        startColumn: word.startColumn,
        endColumn: word.endColumn,
      }
      const line = model.getLineContent(position.lineNumber).slice(0, position.column - 1)
      const assignment = line.match(/^\s*([\w-]+)\s*=\s*["']?\w*$/)
      if (assignment) {
        const field = schema?.properties?.[assignment[1]]
        const values =
          (field && resolve(field).enum) ||
          (field && resolve(field).type === 'boolean' ? ['true', 'false'] : [])
        return {
          suggestions: values.map((value) => ({
            label: String(value),
            kind: monaco.languages.CompletionItemKind.Value,
            insertText: String(value),
            range,
          })),
        }
      }
      return {
        suggestions: Object.entries(schema?.properties || {}).map(([key, raw]) => {
          const field = resolve(raw)
          const value =
            field.type === 'boolean'
              ? '${1:false}'
              : field.type === 'integer' || field.type === 'number'
                ? '${1:1}'
                : field.type === 'array'
                  ? '[${1}]'
                  : field.type === 'object'
                    ? '{ ${1} }'
                    : '"${1}"'
          return {
            label: key,
            kind: monaco.languages.CompletionItemKind.Property,
            documentation: field.description || raw.description,
            detail: field.type,
            insertText: `${key} = ${value}`,
            insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet,
            range,
          }
        }),
      }
    },
  })
}
