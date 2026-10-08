import { MySQL } from 'dt-sql-parser/dist/parser/mysql/index'
import { PostgreSQL } from 'dt-sql-parser/dist/parser/postgresql/index'
const mysql = new MySQL()
const postgres = new PostgreSQL()
self.onmessage = (event: MessageEvent<{ text: string; dialect: string }>) => {
  const { text, dialect } = event.data
  if (new TextEncoder().encode(text).length > 65536) {
    self.postMessage({ issues: [], failed: true })
    return
  }
  try {
    const parser = dialect === 'postgresql' ? postgres : mysql
    self.postMessage({
      failed: false,
      issues: parser
        .validate(text.trimEnd().endsWith(';') ? text : text + ';')
        .slice(0, 20)
        .map((issue) => ({
          line: issue.startLine,
          column: issue.startColumn + 1,
          endLine: issue.endLine,
          endColumn: issue.endColumn + 1,
          message: issue.message.slice(0, 512),
        })),
    })
  } catch {
    self.postMessage({ issues: [], failed: true })
  }
}

self.postMessage({ ready: true })
