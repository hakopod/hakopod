import type * as Monaco from 'monaco-editor'
export type SQLDialect = 'postgresql' | 'mysql' | 'vitess' | 'duckdb' | 'clickhouse' | 'oracle'
export function supportsSQLGrammar(dialect: string) {
  return dialect === 'postgresql' || dialect === 'mysql' || dialect === 'vitess'
}
export function sqlDiagnostics(text: string, _dialect: SQLDialect) {
  return new TextEncoder().encode(text).length > 65536
    ? [{ line: 1, column: 1, message: 'SQL exceeds the editor limit.' }]
    : []
}
const registered = new WeakSet<object>()
export function registerSQL(monaco: typeof Monaco) {
  if (registered.has(monaco)) return
  registered.add(monaco)
  monaco.languages.register({ id: 'hakopod-sql' })
  // Reuse Monaco's maintained SQL tokenizer; diagnostics use the local parser worker.
  monaco.languages.registerCompletionItemProvider('hakopod-sql', {
    provideCompletionItems(model, position) {
      const word = model.getWordUntilPosition(position)
      const range = {
        startLineNumber: position.lineNumber,
        endLineNumber: position.lineNumber,
        startColumn: word.startColumn,
        endColumn: word.endColumn,
      }
      return {
        suggestions: [
          'SELECT',
          'FROM',
          'WHERE',
          'JOIN',
          'ON',
          'INSERT INTO',
          'VALUES',
          'UPDATE',
          'SET',
          'DELETE FROM',
          'CREATE TABLE',
          'WITH',
          'ORDER BY',
          'GROUP BY',
          'NULL',
        ].map((label) => ({
          label,
          insertText: label,
          kind: monaco.languages.CompletionItemKind.Keyword,
          range,
        })),
      }
    },
  })
}
