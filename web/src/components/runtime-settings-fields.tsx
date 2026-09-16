import { RequestError } from './shared'
import { fieldError } from '../lib/form-errors'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { importDotenv, MAX_ENV_FILE_BYTES } from '../lib/dotenv'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'
import { Icon } from './icons'
import {
  isSecretEnvironment,
  sensitiveEnvironment,
  type EnvironmentRow,
} from '../lib/service-environment'

export function EnvironmentFields({
  rows,
  onChange,
  label,
  disabled = false,
  allowSecrets = true,
  error,
  onBusyChange,
}: {
  rows: EnvironmentRow[]
  onChange: (rows: EnvironmentRow[]) => void
  label: string
  disabled?: boolean
  allowSecrets?: boolean
  error?: string
  onBusyChange?: (busy: boolean) => void
}) {
  const fileInput = useRef<HTMLInputElement>(null)
  const container = useRef<HTMLDivElement>(null)
  const movedFocus = useRef<{ row: string; control: string } | null>(null)
  useLayoutEffect(() => {
    const target = movedFocus.current
    movedFocus.current = null
    if (target)
      container.current
        ?.querySelector<HTMLElement>(
          `[data-env-row="${CSS.escape(target.row)}"] [data-env-control="${CSS.escape(target.control)}"]`,
        )
        ?.focus()
  }, [rows])
  const latest = useRef({ rows, disabled, allowSecrets })
  latest.current = { rows, disabled, allowSecrets }
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const [importing, setImporting] = useState(false)
  const [importMessage, setImportMessage] = useState('')
  const [importError, setImportError] = useState(false)
  const [paste, setPaste] = useState('')
  const [revealed, setRevealed] = useState<Set<string>>(() => new Set())
  const locked = disabled || importing
  const plainRows = rows.filter((row) => !row.secret)
  const secretRows = rows.filter((row) => row.secret)
  const update = (id: string, change: Partial<EnvironmentRow>) =>
    onChange(rows.map((row) => (row.id === id ? { ...row, ...change } : row)))
  const changeValue = (row: EnvironmentRow, value: string) => {
    if (allowSecrets && !isSecretEnvironment(row) && sensitiveEnvironment(row.name, value))
      movedFocus.current = { row: row.id, control: 'value' }
    update(row.id, { value })
  }
  const promote = (row: EnvironmentRow, event: React.FocusEvent<HTMLElement>) => {
    if (allowSecrets && !row.secret && sensitiveEnvironment(row.name, row.value)) {
      // Moving between sections remounts the row. Retain the user's next
      // keyboard or pointer target instead of dropping focus onto the page.
      const target = event.relatedTarget
      if (
        target instanceof HTMLElement &&
        target.closest('[data-env-row]') === event.currentTarget.closest('[data-env-row]') &&
        target.dataset.envControl
      ) {
        // Let an in-row action receive its click before moving the row.
        if (target instanceof HTMLButtonElement) return
        movedFocus.current = { row: row.id, control: target.dataset.envControl }
      }
      update(row.id, { secret: true })
    }
  }
  function showError(error: unknown) {
    setImportError(true)
    setImportMessage(error instanceof Error ? error.message : 'Could not import these values.')
  }
  function importText(text: string, initial = rows) {
    const merged = importDotenv(text, initial, allowSecrets)
    const added = merged.filter((row) => !initial.some((before) => before.id === row.id))
    const secrets = added.filter(isSecretEnvironment).length
    const variables = added.length - secrets
    onChange(merged)
    // Never leave imported secret values in the plain .env editor.
    setPaste('')
    setRevealed(new Set())
    setImportError(false)
    setImportMessage(
      `Imported ${variables} ${variables === 1 ? 'variable' : 'variables'}${allowSecrets ? ` and ${secrets} ${secrets === 1 ? 'secret' : 'secrets'}` : ''}. Review before saving.`,
    )
  }
  function looksLikeDotenv(text: string) {
    const first = text
      .replace(/^\uFEFF/, '')
      .split(/\r\n?|\n/)
      .map((line) => line.trim())
      .find((line) => line && !line.startsWith('#'))
    return /^(?:export\s+)?[A-Za-z_]\w*\s*=/.test(first || '')
  }
  function pasteFile(
    event: React.ClipboardEvent<HTMLInputElement | HTMLTextAreaElement>,
    fullFile = false,
  ) {
    const text = event.clipboardData.getData('text')
    if (locked || !looksLikeDotenv(text) || (!fullFile && !/[\r\n]/.test(text))) return
    event.preventDefault()
    try {
      importText(text)
    } catch (error) {
      setPaste(text)
      showError(error)
    }
  }
  function pasteValue(
    event: React.ClipboardEvent<HTMLInputElement | HTMLTextAreaElement>,
    row: EnvironmentRow,
  ) {
    const text = event.clipboardData.getData('text')
    if (
      !locked &&
      allowSecrets &&
      /[\r\n]/.test(text) &&
      !looksLikeDotenv(text) &&
      (row.secret || sensitiveEnvironment(row.name, text))
    ) {
      // Password inputs strip newlines. Keep pasted private keys intact in
      // memory while their masked field stays hidden until explicitly shown.
      event.preventDefault()
      movedFocus.current = { row: row.id, control: 'value' }
      update(row.id, { value: text, secret: true })
      return
    }
    pasteFile(event)
  }
  const renderRow = (row: EnvironmentRow, index: number, secret: boolean) => {
    const protectedValue = allowSecrets && isSecretEnvironment(row)
    const visible = revealed.has(row.id)
    const valueLabel = `${label} ${secret ? 'secret' : 'variable'} ${index + 1}`
    return (
      <div
        className="grid min-w-0 items-start gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_auto]"
        key={row.id}
        data-env-row={row.id}
      >
        <label>
          Name
          <Input
            aria-label={`${valueLabel} name`}
            data-env-control="name"
            value={row.name}
            disabled={locked}
            maxLength={128}
            placeholder={secret ? 'DATABASE_PASSWORD' : 'APP_ENV'}
            autoComplete="off"
            spellCheck={false}
            onChange={(event) => update(row.id, { name: event.target.value })}
            onBlur={(event) => promote(row, event)}
            onPaste={pasteFile}
          />
        </label>
        <div className="grid min-w-0 gap-1">
          <label>
            Value
            {protectedValue && !visible ? (
              <Input
                type="password"
                aria-label={`${valueLabel} secret value`}
                data-env-control="value"
                value={row.value.includes('\n') ? '' : row.value}
                placeholder={
                  row.value.includes('\n')
                    ? 'Multiline secret · show to edit'
                    : 'Enter secret value'
                }
                readOnly={row.value.includes('\n')}
                autoComplete="new-password"
                disabled={locked}
                onChange={(event) => changeValue(row, event.target.value)}
                onBlur={(event) => promote(row, event)}
                onPaste={(event) => pasteValue(event, row)}
              />
            ) : (
              <Textarea
                className="runtime-variable-value"
                aria-label={`${valueLabel} value`}
                data-env-control="value"
                value={row.value}
                disabled={locked}
                rows={secret ? 2 : 1}
                maxLength={secret ? 65536 : 4096}
                placeholder={secret ? 'Paste a secret, including multiline values' : 'production'}
                autoComplete="off"
                spellCheck={false}
                onChange={(event) => changeValue(row, event.target.value)}
                onBlur={(event) => promote(row, event)}
                onPaste={(event) => pasteValue(event, row)}
              />
            )}
          </label>
          {protectedValue ? (
            <Button
              variant="ghost"
              size="sm"
              className="justify-self-start"
              disabled={locked}
              aria-label={`${visible ? 'Hide' : 'Show'} ${valueLabel} value`}
              aria-pressed={visible}
              data-env-control="visibility"
              onBlur={(event) => promote(row, event)}
              onClick={() => {
                if (!row.secret) {
                  movedFocus.current = { row: row.id, control: 'visibility' }
                  update(row.id, { secret: true })
                }
                setRevealed((before) => {
                  const next = new Set(before)
                  if (next.has(row.id)) next.delete(row.id)
                  else next.add(row.id)
                  return next
                })
              }}
            >
              {visible ? 'Hide value' : 'Show value'}
            </Button>
          ) : allowSecrets ? (
            <Button
              variant="ghost"
              size="sm"
              className="justify-self-start"
              disabled={locked}
              aria-label={`Store ${valueLabel} as secret`}
              onClick={() => {
                movedFocus.current = { row: row.id, control: 'value' }
                update(row.id, { secret: true })
              }}
            >
              Store as secret
            </Button>
          ) : null}
        </div>
        <Button
          variant="ghost"
          size="icon"
          disabled={locked}
          className="sm:mt-6"
          aria-label={`Remove ${valueLabel}`}
          data-env-control="remove"
          onBlur={(event) => promote(row, event)}
          onClick={() => onChange(rows.filter((item) => item.id !== row.id))}
        >
          <Icon name="trash" size={14} />
        </Button>
      </div>
    )
  }
  return (
    <div ref={container} className="grid min-w-0 gap-4">
      {error && <RequestError error={error} />}
      <section className="grid min-w-0 gap-3" aria-label={`${label} environment variables`}>
        <h3 className="text-sm font-semibold">
          Environment variables{' '}
          <span className="font-normal text-muted-foreground">{plainRows.length} / 128</span>
        </h3>
        {plainRows.map((row, index) => renderRow(row, index, false))}
        <Button
          className="justify-self-start"
          disabled={locked || plainRows.length >= 128}
          onClick={() => onChange([...rows, { id: crypto.randomUUID(), name: '', value: '' }])}
        >
          <Icon name="plus" size={14} />
          Add variable
        </Button>
      </section>
      {allowSecrets && (
        <section className="grid min-w-0 gap-3" aria-label={`${label} secrets`}>
          <h3 className="text-sm font-semibold">
            Secrets{' '}
            <span className="font-normal text-muted-foreground">{secretRows.length} / 32</span>
          </h3>
          {secretRows.map((row, index) => renderRow(row, index, true))}
          <Button
            className="justify-self-start"
            disabled={locked || secretRows.length >= 32}
            onClick={() =>
              onChange([...rows, { id: crypto.randomUUID(), name: '', value: '', secret: true }])
            }
          >
            <Icon name="plus" size={14} />
            Add secret
          </Button>
          <p className="field-help">
            Secret values are stored separately. Only their references enter your configuration and
            deployment history.
          </p>
        </section>
      )}
      <div className="grid min-w-0 gap-2">
        <div className="flex flex-wrap gap-2">
          <input
            ref={fileInput}
            type="file"
            className="hidden!"
            accept=".env,.txt,text/plain"
            aria-label={`Import ${label} environment file`}
            disabled={locked}
            onChange={async (event) => {
              const file = event.target.files?.[0]
              event.target.value = ''
              if (!file || locked) return
              const initialRows = rows
              setImporting(true)
              onBusyChange?.(true)
              setImportMessage('')
              try {
                if (file.size > MAX_ENV_FILE_BYTES)
                  throw new Error('Choose a .env file smaller than 512 KiB.')
                const text = await file.text()
                if (!mounted.current) return
                if (
                  latest.current.rows !== initialRows ||
                  latest.current.allowSecrets !== allowSecrets
                )
                  throw new Error(
                    'The variables changed while reading the file. Import it again to use the latest values.',
                  )
                importText(text, initialRows)
              } catch (error) {
                if (mounted.current) showError(error)
              } finally {
                if (mounted.current) setImporting(false)
                onBusyChange?.(false)
              }
            }}
          />
          <Button disabled={locked} onClick={() => fileInput.current?.click()}>
            {importing ? 'Importing…' : 'Import .env'}
          </Button>
        </div>
        <details className="grid gap-2">
          <summary className="cursor-pointer text-sm">Paste .env</summary>
          <Textarea
            aria-label={`Paste ${label} .env`}
            value={paste}
            onChange={(event) => setPaste(event.target.value)}
            onPaste={(event) => {
              if (!paste) pasteFile(event, true)
            }}
            disabled={locked}
            rows={4}
            placeholder={'APP_ENV=production\nDATABASE_PASSWORD=your-secret'}
            autoComplete="off"
            spellCheck={false}
          />
          <Button
            className="mt-2"
            disabled={locked || !paste.trim()}
            onClick={() => {
              try {
                importText(paste)
              } catch (error) {
                showError(error)
              }
            }}
          >
            Import pasted variables
          </Button>
        </details>
        {importMessage && (
          <p className="field-help" role={importError ? 'alert' : 'status'}>
            {importMessage}
          </p>
        )}
        <p className="field-help">
          {allowSecrets
            ? 'Paste or import .env to fill both sections. Detected passwords, tokens, private keys and credential URLs move to Secrets automatically. You can also mark any other value as a secret.'
            : 'These values replace Compose placeholders, not service variables. Use application secret references for passwords and tokens.'}{' '}
          Existing names are kept; duplicates must be resolved. References such as ${'{NAME}'} stay
          literal.
        </p>
      </div>
    </div>
  )
}

export function RunCommandFields({
  command,
  args,
  onChange,
  label,
  disabled = false,
  error = '',
  fieldPath = '',
}: {
  command: string
  args: string
  onChange: (value: { command: string; args: string }) => void
  label: string
  disabled?: boolean
  error?: string
  fieldPath?: string
}) {
  return (
    <div className="grid min-w-0 gap-3">
      <div className="grid gap-3 sm:grid-cols-2">
        <label>
          Run command (optional)
          <Input
            aria-label={`${label} run command`}
            value={command}
            error={fieldError(error, `${fieldPath ? fieldPath + '.' : ''}command`)}
            disabled={disabled}
            maxLength={8192}
            placeholder="Use image default, or enter uvicorn"
            onChange={(event) => onChange({ command: event.target.value, args })}
          />
        </label>
        <label>
          Run arguments (optional)
          <Input
            aria-label={`${label} run arguments`}
            value={args}
            error={fieldError(error, `${fieldPath ? fieldPath + '.' : ''}args`)}
            disabled={disabled}
            maxLength={16384}
            placeholder="main:app --host 0.0.0.0 --port 8000"
            onChange={(event) => onChange({ command, args: event.target.value })}
          />
        </label>
      </div>
      <p className="field-help">
        Leave a field blank to use the container image’s default. Quotes keep words together in one
        argument. Your server should listen on 0.0.0.0 and the service port above.
      </p>
    </div>
  )
}
