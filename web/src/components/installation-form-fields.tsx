import { useEffect, useRef, type ReactNode } from 'react'
import type { SecretAction } from '../lib/installation-settings'
import { Input } from './ui/input'
import { SelectField } from './ui/select'

export function InstallationCredential({
  label,
  configured,
  action,
  onAction,
  value,
  onValue,
  maxLength = 8192,
}: {
  label: string
  configured: boolean
  action: SecretAction
  onAction: (value: SecretAction) => void
  value: string
  onValue: (value: string) => void
  maxLength?: number
}) {
  return (
    <div className="grid gap-3">
      <label>
        {label}
        <SelectField
          label={`${label} action`}
          value={action}
          onValueChange={(next) => {
            onAction(next as SecretAction)
            onValue('')
          }}
          options={[
            {
              value: 'keep',
              label: configured ? 'Keep stored credential' : 'No stored credential',
            },
            { value: 'replace', label: configured ? 'Replace credential' : 'Add credential' },
            { value: 'clear', label: 'Clear stored credential', disabled: !configured },
          ]}
        />
      </label>
      {action === 'replace' && (
        <label>
          New {label.toLowerCase()}
          <Input
            type="password"
            required
            value={value}
            onChange={(event) => onValue(event.target.value)}
            autoComplete="new-password"
            maxLength={maxLength}
            spellCheck={false}
          />
        </label>
      )}
      <p className="field-help">Stored credentials are never returned to the browser.</p>
    </div>
  )
}

export function InstallationReviewRows({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="m-0 grid gap-3">
      {rows.map(([label, value]) => (
        <div key={label} className="grid gap-1 sm:grid-cols-[11rem_minmax(0,1fr)] sm:gap-3">
          <dt className="muted-text text-sm">{label}</dt>
          <dd className="m-0 min-w-0 break-words text-sm">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

export function credentialSummary(action: SecretAction, configured: boolean) {
  return action === 'replace'
    ? 'Replace with the entered credential'
    : action === 'clear'
      ? 'Clear the stored credential'
      : configured
        ? 'Keep the stored credential'
        : 'None'
}

export function useInstallationFormFocus(review: boolean) {
  const ref = useRef<HTMLFormElement>(null)
  const reviewed = useRef(false)
  useEffect(() => {
    if (review) reviewed.current = true
    if (reviewed.current) {
      ref.current?.focus()
      ref.current?.scrollIntoView({ block: 'start' })
    }
  }, [review])
  return ref
}
