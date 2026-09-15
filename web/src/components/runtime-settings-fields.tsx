import { Button } from './ui/button'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'
import { Icon } from './icons'
import type { EnvironmentRow } from '../lib/service-environment'

export function EnvironmentFields({
  rows,
  onChange,
  label,
  disabled = false,
}: {
  rows: EnvironmentRow[]
  onChange: (rows: EnvironmentRow[]) => void
  label: string
  disabled?: boolean
}) {
  return (
    <div className="grid min-w-0 gap-3">
      <strong className="text-sm">Environment variables</strong>
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
      <div>
        <Button
          disabled={disabled || rows.length >= 128}
          onClick={() => onChange([...rows, { id: crypto.randomUUID(), name: '', value: '' }])}
        >
          <Icon name="plus" size={14} />
          Add variable
        </Button>
      </div>
      <p className="field-help">
        These values are available when the service starts. Passwords and tokens must use
        application secrets; plain variables are saved in deployment history.
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
}: {
  command: string
  args: string
  onChange: (value: { command: string; args: string }) => void
  label: string
  disabled?: boolean
}) {
  return (
    <div className="grid min-w-0 gap-3">
      <div className="grid gap-3 sm:grid-cols-2">
        <label>
          Run command (optional)
          <Input
            aria-label={`${label} run command`}
            value={command}
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
