import { useEffect, useRef, type ReactNode } from 'react'
import type { SecretAction } from '../lib/installation-settings'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { ReviewFacts } from './operation-review'

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
      <p className="field-help">
        {action === 'replace'
          ? configured
            ? 'The new value replaces the stored one when you save and is used from then on. Revoke the old one at its source afterwards.'
            : 'Credentials are write-only and never shown again after saving.'
          : action === 'clear'
            ? 'Saving erases the stored credential; anything that needs it stops working until a new one is added.'
            : 'Stored credentials are write-only and never returned to the browser.'}
      </p>
    </div>
  )
}

export function InstallationReviewRows({ rows }: { rows: [string, ReactNode][] }) {
  return <ReviewFacts facts={rows} />
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
