import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { Fragment, useId, type ComponentProps, type ReactNode } from 'react'
import { parseProcessCommand } from '../lib/process-command'
import { isSecretEnvironment, type EnvironmentRow } from '../lib/service-environment'
import type { Service } from '../lib/types'
import { Button } from './ui/button'
import { Icon } from './icons'

// Shared pieces of the guided create and configure flows. Styles live in create.css.

export function FlowSteps({
  steps,
  current,
  onSelect,
  disabled,
}: {
  steps: readonly string[]
  current: number
  onSelect?: (index: number) => void
  disabled?: boolean
}) {
  return (
    <ol className="create-steps" aria-label="Steps">
      {steps.map((label, index) => (
        <Fragment key={label}>
          {index > 0 && <li aria-hidden="true" className="create-steps-rule" />}
          <li
            className="create-step"
            data-state={index < current ? 'done' : index === current ? 'current' : 'next'}
            aria-current={index === current ? 'step' : undefined}
          >
            {index < current && onSelect ? (
              <button type="button" disabled={disabled} onClick={() => onSelect(index)}>
                <b>
                  <Icon name="check" size={12} />
                </b>
                {label}
                <span className="sr-only"> (completed, return to this step)</span>
              </button>
            ) : (
              <span>
                <b>{index < current ? <Icon name="check" size={12} /> : index + 1}</b>
                {label}
              </span>
            )}
          </li>
        </Fragment>
      ))}
    </ol>
  )
}

export type Choice<T extends string> = {
  value: T
  title: string
  description: ReactNode
  icon?: ReactNode
  disabled?: boolean
  // Shown instead of the description when a choice is unavailable.
  unavailable?: string
}

// An exclusive set of meaningful options: native radios keep arrow-key selection.
export function ChoiceCards<T extends string>({
  label,
  value,
  choices,
  onChange,
  disabled,
  compact,
}: {
  label: string
  value: T | ''
  choices: readonly Choice<T>[]
  onChange: (value: T) => void
  disabled?: boolean
  compact?: boolean
}) {
  const name = useId()
  return (
    <fieldset className="choice-cards" data-compact={compact || undefined} disabled={disabled}>
      <legend className="sr-only">{label}</legend>
      {choices.map((choice) => (
        <label
          key={choice.value}
          className="choice-card interactive"
          data-disabled={choice.disabled || undefined}
        >
          <Brackets />
          <input
            type="radio"
            className="sr-only"
            name={name}
            value={choice.value}
            checked={value === choice.value}
            disabled={choice.disabled}
            onChange={() => onChange(choice.value)}
          />
          {choice.icon && <span className="choice-card-icon">{choice.icon}</span>}
          <span className="choice-card-text">
            <strong>{choice.title}</strong>
            <span>
              {choice.disabled && choice.unavailable ? choice.unavailable : choice.description}
            </span>
          </span>
          <span className="choice-card-check" aria-hidden="true">
            <Icon name="check" size={14} />
          </span>
        </label>
      ))}
    </fieldset>
  )
}

// Pending actions keep their width, name what is happening and block resubmission.
export function PendingButton({
  pending,
  pendingLabel,
  children,
  disabled,
  ...props
}: ComponentProps<typeof Button> & { pending: boolean; pendingLabel: string }) {
  return (
    <Button {...props} disabled={disabled || pending} aria-busy={pending || undefined}>
      <span className="pending-label" data-pending={pending || undefined}>
        <span>{children}</span>
        <span>{pendingLabel}</span>
      </span>
    </Button>
  )
}

export const plural = (count: number, one: string, many = `${one}s`) =>
  `${count} ${count === 1 ? one : many}`

export type ServiceKind = 'web' | 'private' | 'worker' | 'job' | 'function'

export function serviceKind(service: Service): ServiceKind {
  if (service.job) return 'job'
  if (service.serverless) return 'function'
  if (!service.port) return 'worker'
  return service.public ? 'web' : 'private'
}

export const serviceKindLabel: Record<ServiceKind, string> = {
  web: 'Public web service',
  private: 'Private service',
  worker: 'Background worker',
  job: 'Job',
  function: 'Serverless HTTP',
}

export const servicePresets = {
  web: { image: '', port: 80, public: true, size: 'small', replicas: 1 },
  private: { image: '', port: 8080, public: false, size: 'small', replicas: 1 },
  worker: { image: '', port: 0, public: false, size: 'small', replicas: 1 },
} satisfies Record<string, Service>

// Field-level problems in a variable draft, keyed by row id. Mirrors the checks
// made when the draft is saved, so they can be shown next to each row.
export function environmentIssues(rows: EnvironmentRow[], allowSecrets = true) {
  const issues = new Map<string, string>()
  const counts = new Map<string, number>()
  for (const row of rows) counts.set(row.name, (counts.get(row.name) ?? 0) + 1)
  for (const row of rows) {
    if (row.name === '' && row.value === '') continue
    if (!/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(row.name))
      issues.set(
        row.id,
        row.name
          ? 'Use letters, numbers and underscores, starting with a letter or underscore.'
          : 'Enter a name.',
      )
    else if ((counts.get(row.name) ?? 0) > 1)
      issues.set(row.id, `${row.name} appears more than once.`)
    else if (allowSecrets && isSecretEnvironment(row) && !row.value)
      issues.set(row.id, 'Enter the secret value.')
  }
  return issues
}

export type ServiceIssues = { source: string[]; variables: number; total: number }

export function serviceIssues(
  service: Service,
  draft?: { command: string; args: string; variables: EnvironmentRow[] },
): ServiceIssues {
  const source: string[] = []
  if (!service.image.trim()) source.push('Enter a container image.')
  if (draft)
    for (const [label, value] of [
      ['run command', draft.command],
      ['run arguments', draft.args],
    ] as const)
      try {
        parseProcessCommand(value)
      } catch {
        source.push(`Check the ${label}.`)
      }
  const variables = draft ? environmentIssues(draft.variables).size : 0
  return { source, variables, total: source.length + variables }
}

// Suggest an application name from the first image, e.g. ghcr.io/acme/shop-api:1 → shop-api.
export function nameFromImage(image: string) {
  const repository = image.split('@')[0].split('/').pop()?.split(':')[0] || ''
  return repository
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, '-')
    .replace(/^[^a-z]+/, '')
    .slice(0, 40)
    .replace(/-+$/, '')
}

// Plain variables as an editable .env document. Secrets are write-only and never included.
export function formatDotenv(rows: EnvironmentRow[]) {
  return rows
    .filter((row) => !isSecretEnvironment(row) && (row.name || row.value))
    .map(
      (row) =>
        `${row.name}=${
          /^[A-Za-z0-9_./:@+,%=-]*$/.test(row.value)
            ? row.value
            : `"${row.value.replace(/[\\"\n\r\t]/g, (c) => ({ '\\': '\\\\', '"': '\\"', '\n': '\\n', '\r': '\\r', '\t': '\\t' })[c]!)}"`
        }`,
    )
    .join('\n')
}

export function IssueCount({ count, label = 'issue' }: { count: number; label?: string }) {
  if (!count) return null
  return (
    <span className="issue-count">
      <Icon name="alert" size={12} />
      {plural(count, label)}
    </span>
  )
}
