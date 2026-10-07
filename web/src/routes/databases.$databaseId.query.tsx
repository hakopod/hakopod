import { useEffect, useRef, useState, type FormEvent } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useDatabase, useDatabaseQueryCapabilities } from '../lib/databases'
import { canAccess, useResourceScope, useScope } from '../lib/scope'
import { client } from '../lib/client'
import type { components } from '../lib/api.generated'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { ErrorState, Loading, Note } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Textarea } from '../components/ui/textarea'
import { SelectField } from '../components/ui/select'

export const Route = createFileRoute('/databases/$databaseId/query')({ component: Page })
function Page() {
  const id = Route.useParams().databaseId
  return <QueryPage key={id} id={id} />
}
type Result = components['schemas']['DatabaseQueryResult']
type Scalar = string | number | boolean | null
type ExecutionMode = 'transaction' | 'nontransactional'
type Review = {
  sql: string
  parameters: Scalar[]
  revision: number
  executionMode: ExecutionMode
  maxRows: number
}
function QueryPage({ id }: { id: string }) {
  const database = useDatabase(id)
  const capabilities = useDatabaseQueryCapabilities(id, Boolean(database.data))
  const capability = capabilities.data
  const { identity } = useScope()
  const d = database.data
  useResourceScope(d)
  const [sql, setSQL] = useState('')
  const [parameters, setParameters] = useState('[]')
  const [mode, setMode] = useState('read')
  const [executionMode, setExecutionMode] = useState<ExecutionMode>('transaction')
  const [maxRows, setMaxRows] = useState('100')
  const [review, setReview] = useState<Review | null>(null)
  const [result, setResult] = useState<Result | null>(null)
  const [error, setError] = useState('')
  const [outcome, setOutcome] = useState('')
  const [operation, setOperation] = useState('')
  const [executedSQL, setExecutedSQL] = useState('')
  const [busy, setBusy] = useState(false)
  const lock = useRef(false)
  const controller = useRef<AbortController | null>(null)
  const reviewElement = useRef<HTMLDivElement>(null)
  useEffect(() => () => controller.current?.abort(), [])
  useEffect(() => {
    if (review) reviewElement.current?.focus()
  }, [review])
  const grant = (permission: string) =>
    Boolean(
      d &&
      !identity.application &&
      canAccess(identity, d.project, permission) &&
      (!identity.project || identity.project === d.project) &&
      (!identity.environment || identity.environment === d.environment) &&
      (identity.credential_type !== 'machine' ||
        (identity.project === d.project &&
          identity.environment === d.environment &&
          identity.permissions.includes(permission))),
    )
  const canRead = grant('databases:query')
  const canWrite = grant('databases:write-query')
  useEffect(() => {
    if (!canRead && canWrite) setMode('write')
  }, [canRead, canWrite])
  useEffect(() => {
    if (capability?.execution_modes.length && !capability.execution_modes.includes(executionMode)) {
      setExecutionMode(capability.execution_modes[0])
      setReview(null)
    }
  }, [capability, executionMode])
  const allowed = mode === 'read' ? canRead : canWrite
  const ready = d?.status === 'ready' && !database.isFetching && !database.error
  const writeBlocked = Boolean(d?.recovery && !d.recovery.inspected_at)
  const executable =
    allowed &&
    ready &&
    capability?.supported &&
    !capabilities.error &&
    (mode !== 'read' || capability.read_only_supported === true) &&
    capability.execution_modes.includes(executionMode) &&
    (mode === 'read' || !writeBlocked)
  if (database.isPending) return <Loading />
  if (database.error && !d) return <ErrorState error={database.error} />
  if (!d) return <ErrorState error={new Error('Database unavailable.')} />
  const invalidateReview = () => {
    setReview(null)
    setError('')
  }
  async function execute(statement: string, values: Scalar[], reviewed?: Review) {
    if (!executable || !d || lock.current) return
    if (
      mode === 'write' &&
      (!reviewed || reviewed.revision !== d.revision || reviewed.executionMode !== executionMode)
    )
      return
    lock.current = true
    setBusy(true)
    setError('')
    setResult(null)
    setOutcome('')
    setOperation('')
    setReview(null)
    setExecutedSQL(statement)
    const abort = new AbortController()
    controller.current = abort
    try {
      const response = await client.POST('/databases/{id}/query', {
        signal: abort.signal,
        params: { path: { id } },
        body: {
          sql: statement,
          parameters: values,
          read_only: mode === 'read',
          max_rows: reviewed?.maxRows ?? Number(maxRows),
          max_bytes: 262144,
          expected_revision: reviewed?.revision,
          execution_mode: reviewed?.executionMode ?? executionMode,
        },
      })
      if (!response.response.ok || response.error) {
        const detail = response.error as
          { error?: { message?: string }; operation_id?: string; outcome?: string } | undefined
        setOperation(detail?.operation_id || '')
        setOutcome(detail?.outcome || '')
        if (detail?.outcome === 'unknown') setReview(null)
        setError(detail?.error?.message || `Query failed (${response.response.status}).`)
      } else {
        setResult(response.data || null)
        setOperation(response.data?.operation_id || '')
        setOutcome(response.data?.outcome || '')
        setReview(null)
      }
    } catch {
      if (mode === 'write') setReview(null)
      setOutcome(mode === 'write' ? 'unknown' : 'unavailable')
      setError(
        mode === 'write'
          ? 'The connection ended without a confirmed result. Check the database before retrying.'
          : 'The query connection ended. Your inputs are preserved.',
      )
    } finally {
      setBusy(false)
      lock.current = false
      controller.current = null
    }
  }
  function submit(event: FormEvent) {
    event.preventDefault()
    if (!executable || busy) return
    setError('')
    if (!sql.trim()) {
      setError('Enter a SQL statement.')
      return
    }
    let values: unknown
    try {
      values = JSON.parse(parameters)
    } catch {
      setError('Parameters must be a JSON array.')
      return
    }
    if (
      !Array.isArray(values) ||
      values.length > 100 ||
      values.some(
        (value) => value !== null && !['string', 'number', 'boolean'].includes(typeof value),
      )
    ) {
      setError('Use at most 100 scalar parameters: strings, numbers, booleans or null.')
      return
    }
    if (new TextEncoder().encode(sql).length > 65536) {
      setError('SQL exceeds 65536 bytes.')
      return
    }
    if (
      (values as Scalar[]).some(
        (value) =>
          typeof value === 'number' &&
          (!Number.isFinite(value) || (Number.isInteger(value) && !Number.isSafeInteger(value))),
      )
    ) {
      setError('Quote integers larger than 9007199254740991 to preserve precision.')
      return
    }
    if (!Number.isInteger(Number(maxRows)) || Number(maxRows) < 1 || Number(maxRows) > 1000) {
      setError('Maximum rows must be an integer from 1 to 1000.')
      return
    }
    if (mode === 'write')
      setReview({
        sql,
        parameters: values as Scalar[],
        revision: d!.revision,
        executionMode,
        maxRows: Number(maxRows),
      })
    else void execute(sql, values as Scalar[])
  }
  return (
    <FormPage title="SQL query" breadcrumbs={[]} keepFocusedControlsVisible>
      <div className="flex flex-wrap gap-x-4 gap-y-1 pb-4 text-sm">
        <strong className="break-all">{d.spec.name}</strong>
        <span>
          {d.project} / {d.environment}
        </span>
        <span>{d.spec.engine}</span>
      </div>
      {capabilities.isPending ? (
        <Loading />
      ) : capabilities.error ? (
        <ErrorState error={capabilities.error} />
      ) : !capability?.supported ? (
        <Note>SQL queries are unavailable for this database engine.</Note>
      ) : !canRead && !canWrite ? (
        <Note>Your account does not have permission to query this database.</Note>
      ) : (
        <form onSubmit={submit} className="min-w-0">
          {!capability.read_only_supported && (
            <Note>
              {canWrite
                ? 'This database cannot enforce read-only queries. To run SQL, select Read and write and review the statement.'
                : 'This database requires SQL write permission for all queries. Your account does not have that permission.'}
            </Note>
          )}
          <FormSection
            title="Statement"
            description="Submit one SQL statement. Queries have a 20-second deadline. Results are limited to 256 KiB. Transaction control statements are unavailable."
          >
            <div className="grid gap-4 sm:grid-cols-2">
              <label>
                Mode
                <SelectField
                  label="Query mode"
                  value={mode}
                  disabled={busy}
                  onValueChange={(value) => {
                    setMode(value)
                    invalidateReview()
                  }}
                  options={[
                    {
                      value: 'read',
                      label: 'Read only',
                      disabled: !canRead || !capability.read_only_supported,
                    },
                    { value: 'write', label: 'Read and write', disabled: !canWrite },
                  ]}
                />
              </label>
              <label>
                Maximum rows
                <Input
                  type="number"
                  min={1}
                  max={1000}
                  required
                  value={maxRows}
                  disabled={busy}
                  onChange={(event) => {
                    setMaxRows(event.target.value)
                    invalidateReview()
                  }}
                />
              </label>
            </div>
            {capability.execution_modes.length > 1 && (
              <label>
                Execution
                <SelectField
                  label="SQL execution mode"
                  value={executionMode}
                  disabled={busy}
                  onValueChange={(value) => {
                    setExecutionMode(value as ExecutionMode)
                    invalidateReview()
                  }}
                  options={capability.execution_modes.map((value) => ({
                    value,
                    label: value === 'transaction' ? 'Transaction' : 'Without a transaction',
                  }))}
                />
              </label>
            )}
            <label>
              SQL
              <Textarea
                required
                value={sql}
                rows={8}
                disabled={busy}
                className="font-mono"
                aria-describedby="sql-instruction"
                onChange={(event) => {
                  setSQL(event.target.value)
                  invalidateReview()
                }}
              />
            </label>
            <p id="sql-instruction" className="text-sm text-muted-foreground">
              {parameterInstruction(capability.parameter_style)}
            </p>
            <label>
              Parameters
              <Textarea
                value={parameters}
                rows={3}
                disabled={busy}
                className="font-mono"
                aria-describedby="parameter-instruction"
                onChange={(event) => {
                  setParameters(event.target.value)
                  invalidateReview()
                }}
              />
            </label>
            <p id="parameter-instruction" className="text-sm text-muted-foreground">
              JSON array. Quote large integers to preserve precision.
            </p>
            {mode === 'write' && (
              <Note>
                {executionMode === 'nontransactional'
                  ? 'This statement runs without a transaction. Changes can persist if execution fails or the connection closes. The API cannot roll them back.'
                  : 'Writes commit one transaction. Check the statement and target before execution. A lost connection during commit can leave the outcome unknown.'}
              </Note>
            )}
            {capability.ddl_commit === 'implicit_commit' && (
              <p className="text-sm text-muted-foreground">
                Schema changes require execution without a transaction. The database can commit them
                immediately.
              </p>
            )}
            {d.spec.engine === 'postgresql' && (
              <p className="text-sm text-muted-foreground">
                PostgreSQL statements must support EXPLAIN. COPY is unavailable.
              </p>
            )}
            {!ready && (
              <Note>
                {database.error
                  ? 'Refresh failed. Query execution is disabled until the database refresh succeeds.'
                  : 'The database must be ready before queries can run.'}
              </Note>
            )}
            {mode === 'write' && writeBlocked && (
              <Note>Inspect the restored data before running writes.</Note>
            )}
          </FormSection>
          {review && (
            <div
              ref={reviewElement}
              tabIndex={-1}
              className="mt-4 outline-offset-2 focus:outline-2 focus:outline-current"
            >
              <FormSection title="Review write">
                <p className="break-all">
                  {d.spec.name} · {d.project} / {d.environment} · revision {review.revision}
                </p>
                <p>
                  {review.executionMode === 'transaction'
                    ? 'One transaction'
                    : 'Without a transaction'}{' '}
                  · Maximum {review.maxRows} result rows
                </p>
                {review.executionMode === 'nontransactional' && (
                  <Note>
                    Changes can persist after an error. Confirm that this execution mode is
                    appropriate.
                  </Note>
                )}
                <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded border p-3 text-sm">
                  {review.sql}
                </pre>
                <pre className="max-h-32 overflow-auto whitespace-pre-wrap break-all rounded border p-3 text-sm">
                  {JSON.stringify(review.parameters, null, 2)}
                </pre>
                {review.revision !== d.revision && (
                  <Note>The database changed. Review the statement again.</Note>
                )}
                <div className="flex flex-wrap gap-2">
                  <Button
                    type="button"
                    variant="primary"
                    disabled={
                      busy ||
                      !executable ||
                      review.revision !== d.revision ||
                      review.executionMode !== executionMode
                    }
                    onClick={() => void execute(review.sql, review.parameters, review)}
                  >
                    Execute write
                  </Button>
                  <Button type="button" disabled={busy} onClick={() => setReview(null)}>
                    Edit statement
                  </Button>
                </div>
              </FormSection>
            </div>
          )}
          {error && <FormError>{error}</FormError>}
          <div className="form-footer">
            <Button type="submit" variant="primary" disabled={busy || !executable}>
              {busy ? 'Running query…' : mode === 'write' ? 'Review write' : 'Run query'}
            </Button>
          </div>
        </form>
      )}
      {(outcome || operation) && (
        <div className="py-4 text-sm">
          <div role="status" className="flex flex-wrap gap-x-4 gap-y-1">
            <span>Last query outcome: {outcome || 'unavailable'}</span>
            {operation && <span className="break-all">Operation: {operation}</span>}
          </div>
          <details className="mt-2">
            <summary className="cursor-pointer">Executed statement</summary>
            <pre className="mt-2 max-h-32 overflow-auto whitespace-pre-wrap break-all">
              {executedSQL}
            </pre>
          </details>
        </div>
      )}
      {result && (
        <FormSection title="Result">
          <p>
            {result.rows_affected} {result.rows_affected === 1 ? 'row' : 'rows'} affected.{' '}
            {result.rows.length} {result.rows.length === 1 ? 'row' : 'rows'} returned.
          </p>
          {result.truncated && (
            <Note>The result reached its row or byte limit. Additional rows are omitted.</Note>
          )}
          <div
            className="max-h-96 overflow-auto outline-offset-2 focus:outline-2 focus:outline-current"
            tabIndex={0}
            aria-label="Query result table"
          >
            <table className="w-full text-left text-sm">
              <thead>
                <tr>
                  {result.columns.map((column, index) => (
                    <th key={index} scope="col" className="border-b px-3 py-2">
                      {column.name}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {result.rows.map((row, index) => (
                  <tr key={index}>
                    {row.map((value, cell) => (
                      <td key={cell} className="max-w-96 break-all border-b px-3 py-2 font-mono">
                        {value === null ? (
                          <span className="text-muted-foreground" aria-label="SQL null">
                            NULL
                          </span>
                        ) : (
                          value
                        )}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {result.rows.length === 0 && <p>No result rows.</p>}
        </FormSection>
      )}
    </FormPage>
  )
}

function parameterInstruction(style: string) {
  switch (style) {
    case '$1':
      return 'Use $1, $2 and later placeholders for parameters.'
    case '?':
      return 'Use one ? placeholder for each parameter, in array order.'
    case ':1':
      return 'Use :1, :2 and later placeholders for input parameters.'
    case '{p1:Type}':
      return 'Use typed placeholders such as {p1:String} and {p2:Int64}, in array order.'
    default:
      return 'Use the parameter format supported by this database engine.'
  }
}
