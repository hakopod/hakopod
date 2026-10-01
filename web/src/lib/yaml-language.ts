import { parseDocument, LineCounter } from 'yaml'
import type * as Monaco from 'monaco-editor'
export function yamlDiagnostics(source: string) {
  const lineCounter = new LineCounter()
  const document = parseDocument(source, { lineCounter })
  return document.errors
    .slice(0, 16)
    .map((error) => ({
      message: 'Invalid YAML syntax. Check indentation and this position.',
      ...lineCounter.linePos(error.pos[0]),
    }))
}
let registered = false
export function registerYAML(monaco: typeof Monaco) {
  if (registered) return
  registered = true
  monaco.languages.register({ id: 'hakopod-yaml', extensions: ['.yml', '.yaml'] })
  monaco.languages.setMonarchTokensProvider('hakopod-yaml', {
    tokenizer: {
      root: [
        [/#.*$/, 'comment'],
        [/\$\{\{.*?\}\}/, 'variable'],
        [/"([^"\\]|\\.)*"|'[^']*'/, 'string'],
        [/\b(true|false|null)\b/, 'keyword'],
        [/\b\d+(\.\d+)?\b/, 'number'],
        [/[\w.-]+(?=\s*:)/, 'key'],
        [/[\[\]{},:|>-]/, 'delimiter'],
      ],
    },
  })
  monaco.languages.setLanguageConfiguration('hakopod-yaml', {
    comments: { lineComment: '#' },
    brackets: [
      ['[', ']'],
      ['{', '}'],
    ],
  })
  monaco.languages.registerCompletionItemProvider('hakopod-yaml', {
    provideCompletionItems(model, position) {
      const compose = model.uri.path.includes('compose')
      const keys = compose
        ? [
            'services',
            'image',
            'ports',
            'environment',
            'volumes',
            'networks',
            'command',
            'depends_on',
            'build',
            'restart',
          ]
        : [
            'name',
            'on',
            'jobs',
            'runs-on',
            'steps',
            'uses',
            'run',
            'with',
            'env',
            'permissions',
            'needs',
            'if',
            'strategy',
            'script',
            'stages',
          ]
      const word = model.getWordUntilPosition(position)
      return {
        suggestions: keys.map((key) => ({
          label: key,
          kind: monaco.languages.CompletionItemKind.Property,
          insertText: `${key}: `,
          range: {
            startLineNumber: position.lineNumber,
            endLineNumber: position.lineNumber,
            startColumn: word.startColumn,
            endColumn: word.endColumn,
          },
        })),
      }
    },
  })
}
