import { RequestError } from './shared'
import { fieldError } from '../lib/form-errors'
import { useRef, useState } from 'react'
import { importDotenv, MAX_ENV_FILE_BYTES } from '../lib/dotenv'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'
import { Icon } from './icons'
import { sensitiveEnvironment, type EnvironmentRow } from '../lib/service-environment'

export function EnvironmentFields({
  rows,
  onChange,
  label,
  disabled = false,
  allowSecrets = true,
  error,
}: {
  rows: EnvironmentRow[]
  onChange: (rows: EnvironmentRow[]) => void
  label: string
  disabled?: boolean
  allowSecrets?: boolean
  error?: string
}) {
  const fileInput = useRef<HTMLInputElement>(null)
  const latest = useRef({ rows, disabled })
  latest.current = { rows, disabled }
  const [importing, setImporting] = useState(false)
  const [importMessage, setImportMessage] = useState('')
  const [importError, setImportError] = useState(false)
  const [paste, setPaste] = useState('')
  return (
    <div className="grid min-w-0 gap-3">
      <strong className="text-sm">Environment variables</strong>
      {error && <RequestError error={error} />}
      {rows.map((row, index) => (
        <div
          className="grid min-w-0 items-start gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_auto]"
          key={row.id}
        >
          <label>
            Name
            <Input
              aria-label={`${label} variable ${index + 1} name`}
              value={row.name}
              disabled={disabled}
              maxLength={128}
              placeholder="APP_ENV"
              onChange={(event) =>
                onChange(
                  rows.map((item) =>
                    item.id === row.id ? { ...item, name: event.target.value } : item,
                  ),
                )
              }
            />
          </label>
          <label>
            Value
            {sensitiveEnvironment(row.name, row.value) ? (
              <Input
                type="password"
                aria-label={`${label} variable ${index + 1} secret value`}
                value={row.value}
                autoComplete="new-password"
                disabled={disabled}
                onChange={(event) =>
                  onChange(
                    rows.map((item) =>
                      item.id === row.id ? { ...item, value: event.target.value } : item,
                    ),
                  )
                }
              />
            ) : (
              <Textarea
                className="runtime-variable-value"
                aria-label={`${label} variable ${index + 1} value`}
                value={row.value}
                disabled={disabled}
                rows={1}
                maxLength={4096}
                placeholder="production"
                autoComplete="off"
                onChange={(event) =>
                  onChange(
                    rows.map((item) =>
                      item.id === row.id ? { ...item, value: event.target.value } : item,
                    ),
                  )
                }
              />
            )}
            {sensitiveEnvironment(row.name, row.value) && (
              <span className="field-help">
                {allowSecrets
                  ? 'Stored as an application secret when reviewed.'
                  : 'Use an application secret reference instead of a Compose interpolation value.'}
              </span>
            )}
          </label>
          <Button
            variant="ghost"
            size="icon"
            disabled={disabled}
            className="sm:mt-6"
            aria-label={`Remove ${label} variable ${index + 1}`}
            onClick={() => onChange(rows.filter((item) => item.id !== row.id))}
          >
            <Icon name="trash" size={14} />
          </Button>
        </div>
      ))}
      <div className="flex flex-wrap gap-2">
        <Button
          disabled={disabled || rows.length >= 128}
          onClick={() => onChange([...rows, { id: crypto.randomUUID(), name: '', value: '' }])}
        >
          <Icon name="plus" size={14} />
          Add variable
        </Button>
        <input
          ref={fileInput}
          type="file"
          className="hidden!"
          accept=".env,.txt,text/plain"
          aria-label={`Import ${label} environment file`}
          onChange={async (event) => {
            const file = event.target.files?.[0]
            event.target.value = ''
            if (!file) return
            const initialRows = rows
            setImporting(true)
            setImportMessage('')
            try {
              if (file.size > MAX_ENV_FILE_BYTES)
                throw new Error('Choose a .env file smaller than 512 KiB.')
              const text = await file.text()
              if (latest.current.rows !== initialRows || latest.current.disabled)
                throw new Error(
                  'The variables changed while reading the file. Import it again to use the latest values.',
                )
              const merged = importDotenv(text, initialRows, allowSecrets)
              onChange(merged)
              setImportError(false)
              setImportMessage(
                `Imported ${merged.length - initialRows.length} variables. Review them before saving.`,
              )
            } catch (error) {
              setImportError(true)
              setImportMessage(
                error instanceof Error ? error.message : 'Could not read this file. Try again.',
              )
            } finally {
              setImporting(false)
            }
          }}
        />
        <Button
          disabled={disabled || importing || rows.length >= 128}
          onClick={() => fileInput.current?.click()}
        >
          {importing ? 'Importing…' : 'Import .env'}
        </Button>
      </div>
      <details className="grid gap-2">
        <summary className="cursor-pointer text-sm">Paste .env</summary>
        <Textarea
          aria-label={`Paste ${label} .env`}
          value={paste}
          onChange={(event) => setPaste(event.target.value)}
          disabled={disabled || importing}
          rows={5}
          placeholder={'APP_ENV=production\nPORT=8000'}
          autoComplete="off"
          spellCheck={false}
        />
        <Button
          className="mt-2"
          disabled={disabled || importing || !paste.trim()}
          onClick={() => {
            try {
              const merged = importDotenv(paste, rows, allowSecrets)
              onChange(merged)
              setPaste('')
              setImportError(false)
              setImportMessage('Variables imported. Review them before saving.')
            } catch (error) {
              setImportError(true)
              setImportMessage(
                error instanceof Error ? error.message : 'Could not import variables.',
              )
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
        Import adds variables without replacing existing names. References such as ${'{NAME}'} are
        kept literally.
      </p>
      <p className="field-help">
        {allowSecrets
          ? 'These values are available when the service starts. Passwords, tokens and credential URLs are stored as application secret references when you review. Plain variables are saved in deployment history.'
          : 'These values substitute Compose placeholders during conversion. They are not automatically injected into services. Use application secret references for passwords and tokens.'}
      </p>
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
