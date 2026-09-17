import { useId } from 'react'
import { Button } from './ui/button'
import type { Service } from '../lib/types'
import { fieldError } from '../lib/form-errors'
import { Input } from './ui/input'

type Resources = NonNullable<Service['resources']>

export function ResourceFields({
  name,
  resources,
  disabled,
  hostedFree,
  error,
  onChange,
}: {
  name: string
  resources?: Resources
  disabled?: boolean
  hostedFree?: boolean
  error?: string
  onChange: (resources: Resources | undefined) => void
}) {
  const helpId = useId()
  const hasOverrides = Boolean(resources && Object.values(resources).some(Boolean))
  const fields = [
    ['cpu_request', 'CPU request', '250m'],
    ['cpu_limit', 'CPU limit', '1'],
    ['memory_request', 'Memory request', '256Mi'],
    ['memory_limit', 'Memory limit', '512Mi'],
  ] as const
  return (
    <details className="group" open={hasOverrides ? true : undefined}>
      <summary className="min-h-11 cursor-pointer py-3 text-sm focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring">
        Custom CPU and memory
      </summary>
      <div className="flex flex-col gap-3 pb-2">
        <p id={helpId} className="field-help">
          {hostedFree
            ? 'Hosted Free uses a fixed resource budget. Connect your own server to customize CPU and memory.'
            : 'Optional, per replica or job attempt. Requests reserve capacity; limits cap usage. Leave a field blank to use its size default. Each request must fit within its limit.'}
        </p>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          {fields.map(([key, label, example]) => (
            <label key={key}>
              {label}
              <Input
                aria-label={`${name} ${label}`}
                aria-describedby={helpId}
                value={resources?.[key] || ''}
                placeholder={`Size default · e.g. ${example}`}
                disabled={disabled || hostedFree}
                maxLength={32}
                error={fieldError(error || '', `services.${name}.resources.${key}`)}
                onChange={(event) => {
                  const next = { ...resources, [key]: event.target.value }
                  if (!event.target.value) delete next[key]
                  onChange(Object.keys(next).length ? next : undefined)
                }}
              />
            </label>
          ))}
        </div>
        {hasOverrides && (
          <Button className="self-start" disabled={disabled} onClick={() => onChange(undefined)}>
            Use size defaults
          </Button>
        )}
        {!hostedFree && (
          <p className="field-help">
            CPU: 1000m = 1 core. Memory: 1024Mi = 1Gi. Available capacity and Cloud allowances still
            apply.
          </p>
        )}
      </div>
    </details>
  )
}
