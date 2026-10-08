import { test } from 'node:test'
import assert from 'node:assert/strict'
import { MySQL } from 'dt-sql-parser/dist/parser/mysql/index'
import { PostgreSQL } from 'dt-sql-parser/dist/parser/postgresql/index'
import { supportsSQLGrammar, sqlDiagnostics } from './sql-language.ts'
test('grammar dialect support is explicit', () => {
  for (const dialect of ['mysql', 'vitess', 'postgresql'])
    assert.equal(supportsSQLGrammar(dialect), true)
  for (const dialect of ['oracle', 'clickhouse', 'duckdb'])
    assert.equal(supportsSQLGrammar(dialect), false)
  assert.equal(sqlDiagnostics('x'.repeat(65537), 'mysql').length, 1)
})
test('maintained grammars distinguish malformed statements', () => {
  for (const parser of [new MySQL(), new PostgreSQL()]) {
    assert.equal(parser.validate('SELECT 1 FROM records;').length, 0, parser.constructor.name)
    assert.ok(parser.validate('SELECT FROM WHERE').length > 0)
  }
  assert.equal(new PostgreSQL().validate('SELECT $1::bigint;').length, 0, 'PostgreSQL bind')
  assert.ok(new MySQL().validate('SELECT ? FROM records;').length > 0) // Bind placeholders are a known advisory grammar limitation.
})
