import { RequestError } from './shared'
import { fieldError } from '../lib/form-errors'
import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { Eye, EyeOff } from 'lucide-react'
import { importDotenv, MAX_ENV_FILE_BYTES } from '../lib/dotenv'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'
import { Icon } from './icons'
import { environmentIssues, formatDotenv, IssueCount } from './create-flow'
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
  showIssues = false,
}: {
  rows: EnvironmentRow[]
  onChange: (rows: EnvironmentRow[]) => void
  label: string
  disabled?: boolean
  allowSecrets?: boolean
  error?: string
  onBusyChange?: (busy: boolean) => void
  // Set once the user tries to leave the step, so problems appear at the boundary.
  showIssues?: boolean
}) {
  const fileInput = useRef<HTMLInputElement>(null)
  const container = useRef<HTMLDivElement>(null)
  const bulkID = useId()
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
  const [bulk, setBulk] = useState<string | null>(null)
  const [revealed, setRevealed] = useState<Set<string>>(() => new Set())
  const locked = disabled || importing
  const plainRows = rows.filter((row) => !row.secret)
  const secretRows = rows.filter((row) => row.secret)
  const issues = environmentIssues(rows, allowSecrets)
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
    setRevealed(new Set())
    setImportError(false)
    setImportMessage(
      `Imported ${variables} ${variables === 1 ? 'variable' : 'variables'}${allowSecrets ? ` and ${secrets} ${secrets === 1 ? 'secret' : 'secrets'}` : ''}. Review before saving.`,
    )
  }
  function applyBulk(text: string) {
    // The editor holds plain variables only; secret rows are kept as they are.
    const kept = rows.filter((row) => row.secret)
    if (!text.trim()) {
      onChange(kept)
      setImportError(false)
      setImportMessage('Removed all plain variables.')
    } else importText(text, kept)
    // Never leave detected secret values in the plain editor.
    setBulk(null)
  }
  function looksLikeDotenv(text: string) {
    const first = text
      .replace(/^﻿/, '')
      .split(/\r\n?|\n/)
      .map((line) => line.trim())
      .find((line) => line && !line.startsWith('#'))
    return /^(?:export\s+)?[A-Za-z_]\w*\s*=/.test(first || '')
  }
  function pasteFile(event: React.ClipboardEvent<HTMLInputElement | HTMLTextAreaElement>) {
    const text = event.clipboardData.getData('text')
    if (locked || !looksLikeDotenv(text) || !/[\r\n]/.test(text)) return
    event.preventDefault()
    try {
      importText(text)
    } catch (error) {
      setBulk(text)
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
    const issue = showIssues ? issues.get(row.id) : undefined
    return (
      <div
        className="env-row"
        key={row.id}
        data-env-row={row.id}
        data-protected={protectedValue || undefined}
      >
        <div className="env-row-name">
          <Input
            aria-label={`${valueLabel} name`}
            data-env-control="name"
            value={row.name}
            disabled={locked}
            maxLength={128}
            placeholder={secret ? 'DATABASE_PASSWORD' : 'APP_ENV'}
            autoComplete="off"
            spellCheck={false}
            error={issue}
            onChange={(event) => update(row.id, { name: event.target.value })}
            onBlur={(event) => promote(row, event)}
            onPaste={pasteFile}
          />
        </div>
        <div className="env-row-value">
          {protectedValue && !visible ? (
            <Input
              type="password"
              aria-label={`${valueLabel} secret value`}
              data-env-control="value"
              value={row.value.includes('\n') ? '' : row.value}
              placeholder={
                row.value.includes('\n') ? 'Multiline secret · show to edit' : 'Write-only value'
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
              placeholder={secret ? 'Paste a secret, including multiline values' : 'Empty value'}
              autoComplete="off"
              spellCheck={false}
              onChange={(event) => changeValue(row, event.target.value)}
              onBlur={(event) => promote(row, event)}
              onPaste={(event) => pasteValue(event, row)}
            />
          )}
        </div>
        <div className="env-row-actions">
          {protectedValue ? (
            <Button
              variant="ghost"
              size="icon"
              disabled={locked}
              aria-label={`${visible ? 'Hide' : 'Show'} ${valueLabel} value`}
              title={visible ? 'Hide value' : 'Show value'}
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
              {visible ? (
                <EyeOff size={16} aria-hidden="true" />
              ) : (
                <Eye size={16} aria-hidden="true" />
              )}
            </Button>
          ) : allowSecrets ? (
            <Button
              variant="ghost"
              size="icon"
              disabled={locked}
              aria-label={`Store ${valueLabel} as secret`}
              title="Store as secret"
              onClick={() => {
                movedFocus.current = { row: row.id, control: 'value' }
                update(row.id, { secret: true })
              }}
            >
              <Icon name="lock" size={15} />
            </Button>
          ) : null}
          <Button
            variant="ghost"
            size="icon"
            disabled={locked}
            aria-label={`Remove ${valueLabel}`}
            title="Remove"
            data-env-control="remove"
            onBlur={(event) => promote(row, event)}
            onClick={() => onChange(rows.filter((item) => item.id !== row.id))}
          >
            <Icon name="trash" size={15} />
          </Button>
        </div>
      </div>
    )
  }
  const plainIssues = plainRows.filter((row) => issues.has(row.id)).length
  const secretIssues = secretRows.filter((row) => issues.has(row.id)).length
  return (
    <div ref={container} className="env-editor">
      {error && <RequestError error={error} />}
      <div className="env-toolbar">
        <Button
          disabled={locked || plainRows.length >= 128}
          onClick={() => onChange([...rows, { id: crypto.randomUUID(), name: '', value: '' }])}
        >
          <Icon name="plus" size={14} />
          Add variable
        </Button>
        {allowSecrets && (
          <Button
            disabled={locked || secretRows.length >= 32}
            onClick={() =>
              onChange([...rows, { id: crypto.randomUUID(), name: '', value: '', secret: true }])
            }
          >
            <Icon name="lock" size={14} />
            Add secret
          </Button>
        )}
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
        <Button variant="ghost" disabled={locked} onClick={() => fileInput.current?.click()}>
          {importing ? 'Importing…' : 'Import .env'}
        </Button>
        <Button
          variant="ghost"
          disabled={locked}
          aria-expanded={bulk !== null}
          aria-controls={bulkID}
          onClick={() => setBulk(bulk === null ? formatDotenv(rows) : null)}
        >
          Bulk edit
        </Button>
      </div>
      {!allowSecrets && (
        <p className="field-help">
          These values replace Compose placeholders, not service variables.
        </p>
      )}
      {bulk !== null && (
        <div className="env-bulk" id={bulkID}>
          <label>
            Plain variables as .env
            <Textarea
              value={bulk}
              onChange={(event) => setBulk(event.target.value)}
              disabled={locked}
              rows={Math.min(14, Math.max(4, bulk.split('\n').length + 1))}
              placeholder={'APP_ENV=production\nLOG_LEVEL=info'}
              autoComplete="off"
              spellCheck={false}
              aria-describedby={`${bulkID}-help`}
            />
          </label>
          <p id={`${bulkID}-help`} className="field-help">
            One NAME=value per line. Applying replaces the plain variables
            {allowSecrets
              ? '; secrets are kept and never shown here. Detected credentials become secrets.'
              : '.'}
          </p>
          <div className="env-bulk-actions">
            <Button
              disabled={locked}
              onClick={() => {
                try {
                  applyBulk(bulk)
                } catch (error) {
                  showError(error)
                }
              }}
            >
              Apply to variables
            </Button>
            <Button variant="ghost" disabled={locked} onClick={() => setBulk(null)}>
              Discard text
            </Button>
          </div>
        </div>
      )}
      {importMessage && (
        <p
          className={importError ? 'field-error' : 'field-help'}
          role={importError ? 'alert' : 'status'}
        >
          {importMessage}
        </p>
      )}
      <section className="env-section" aria-label={`${label} environment variables`}>
        <h3>
          Variables
          <span className="env-count">{plainRows.length} / 128</span>
          <IssueCount count={plainIssues} />
        </h3>
        {plainRows.length > 0 ? (
          <>
            <div className="env-columns" aria-hidden="true">
              <span>Name</span>
              <span>Value</span>
            </div>
            {plainRows.map((row, index) => renderRow(row, index, false))}
          </>
        ) : (
          <p className="field-help">
            No plain variables. Values here are visible in configuration and history.
          </p>
        )}
      </section>
      {allowSecrets && (
        <section className="env-section env-section-secret" aria-label={`${label} secrets`}>
          <h3>
            <Icon name="lock" size={14} />
            Secrets
            <span className="env-count">{secretRows.length} / 32</span>
            <IssueCount count={secretIssues} />
          </h3>
          <p className="field-help">
            Write-only. Values are stored separately; only references enter your configuration and
            deployment history.
          </p>
          {secretRows.length > 0 && (
            <>
              <div className="env-columns" aria-hidden="true">
                <span>Name</span>
                <span>Value</span>
              </div>
              {secretRows.map((row, index) => renderRow(row, index, true))}
            </>
          )}
        </section>
      )}
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
