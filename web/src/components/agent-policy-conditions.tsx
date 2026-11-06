import { useId, useMemo, useRef, useState } from 'react'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { Button } from './ui/button'
import { SelectField } from './ui/select'
import { Icon } from './icons'
import { fieldLabels, type AgentPolicyCondition } from '../lib/agent-policies'
import { fieldHints, policyFields } from '../lib/agent-policy-form'

export type Suggestion = { value: string; label: string; detail?: string }

// A searchable multi-value picker. Known values are chosen from the list; when
// freeText is set, Enter also adds what was typed (names, patterns with *).
export function ValuePicker({
  label,
  values,
  onChange,
  options,
  freeText = true,
  error,
  placeholder,
  mono = false,
}: {
  label: string
  values: string[]
  onChange: (values: string[]) => void
  options: Suggestion[]
  freeText?: boolean
  error?: boolean
  placeholder?: string
  mono?: boolean
}) {
  const [text, setText] = useState('')
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const input = useRef<HTMLInputElement>(null)
  const id = useId()
  const names = useMemo(() => new Map(options.map((o) => [o.value, o.label])), [options])
  const query = text.trim().toLowerCase()
  const matches = options
    .filter((o) => !values.includes(o.value))
    .filter(
      (o) =>
        !query ||
        o.label.toLowerCase().includes(query) ||
        o.value.toLowerCase().includes(query) ||
        o.detail?.toLowerCase().includes(query),
    )
    .slice(0, 50)
  const add = (value: string) => {
    const next = value.trim()
    if (next && !values.includes(next)) onChange([...values, next])
    setText('')
    setActive(0)
    input.current?.focus()
  }
  const typed =
    freeText && query && !options.some((o) => o.value.toLowerCase() === query) ? text.trim() : ''
  return (
    <div className="relative min-w-0">
      <div
        className={`flex min-h-10 min-w-0 cursor-text flex-wrap items-center gap-1.5 rounded-[var(--radius-control)] border bg-[var(--background)] px-2 py-1.5 focus-within:outline-2 focus-within:outline-offset-1 focus-within:outline-[var(--navigation-active)] ${error ? 'border-[var(--destructive)]' : 'border-[var(--hairline)]'}`}
        onClick={() => input.current?.focus()}
      >
        {values.map((value) => (
          <span
            key={value}
            className="inline-flex max-w-full items-center gap-1 rounded-[var(--radius-control)] bg-[var(--surface-2)] py-0.5 pr-0.5 pl-2 text-[13px] leading-5"
            title={names.has(value) && names.get(value) !== value ? value : undefined}
          >
            <span className={`truncate ${mono && !names.has(value) ? 'font-mono' : ''}`}>
              {names.get(value) ?? value}
            </span>
            <button
              type="button"
              className="grid size-5 place-items-center rounded text-[var(--muted-foreground)] hover:text-[var(--foreground)] focus-visible:outline-2"
              aria-label={`Remove ${names.get(value) ?? value}`}
              onClick={(event) => {
                event.stopPropagation()
                onChange(values.filter((v) => v !== value))
              }}
            >
              <Icon name="x" size={12} />
            </button>
          </span>
        ))}
        <input
          ref={input}
          role="combobox"
          aria-label={label}
          aria-expanded={open}
          aria-controls={`${id}-list`}
          aria-autocomplete="list"
          aria-invalid={error || undefined}
          className="h-7 min-w-28 flex-1 bg-transparent px-1 text-sm outline-none placeholder:text-[var(--muted-foreground)]"
          value={text}
          placeholder={values.length ? 'Add another' : placeholder}
          onFocus={() => setOpen(true)}
          onBlur={() => setTimeout(() => setOpen(false), 120)}
          onChange={(event) => {
            setText(event.target.value)
            setActive(0)
            setOpen(true)
          }}
          onKeyDown={(event) => {
            const count = matches.length + (typed ? 1 : 0)
            if (event.key === 'ArrowDown') {
              event.preventDefault()
              setOpen(true)
              setActive((a) => (count ? (a + 1) % count : 0))
            } else if (event.key === 'ArrowUp') {
              event.preventDefault()
              setActive((a) => (count ? (a - 1 + count) % count : 0))
            } else if (event.key === 'Enter' || (event.key === ',' && typed)) {
              event.preventDefault()
              if (typed && active === 0) add(typed)
              else if (matches[active - (typed ? 1 : 0)])
                add(matches[active - (typed ? 1 : 0)].value)
            } else if (event.key === 'Escape') setOpen(false)
            else if (event.key === 'Backspace' && !text && values.length)
              onChange(values.slice(0, -1))
          }}
        />
      </div>
      {open && (typed || matches.length > 0) && (
        <ul
          id={`${id}-list`}
          role="listbox"
          aria-label={label}
          className="absolute top-full right-0 left-0 z-30 mt-1 max-h-64 overflow-auto rounded-[var(--radius-card)] border border-[var(--hairline)] bg-[var(--card)] p-1 shadow-lg"
        >
          {typed && (
            <li
              role="option"
              aria-selected={active === 0}
              className={`cursor-pointer rounded px-2 py-1.5 text-sm ${active === 0 ? 'bg-[var(--surface-2)]' : ''}`}
              onMouseDown={(event) => {
                event.preventDefault()
                add(typed)
              }}
            >
              Add <span className="font-mono">{typed}</span>
            </li>
          )}
          {matches.map((option, index) => {
            const selected = active === index + (typed ? 1 : 0)
            return (
              <li
                key={option.value}
                role="option"
                aria-selected={selected}
                className={`grid cursor-pointer gap-0.5 rounded px-2 py-1.5 ${selected ? 'bg-[var(--surface-2)]' : ''}`}
                onMouseEnter={() => setActive(index + (typed ? 1 : 0))}
                onMouseDown={(event) => {
                  event.preventDefault()
                  add(option.value)
                }}
              >
                <span className="truncate text-sm">{option.label}</span>
                {option.detail && (
                  <span className="truncate font-mono text-xs text-[var(--muted-foreground)]">
                    {option.detail}
                  </span>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

const operatorOptions = (field: AgentPolicyCondition['field']) => [
  { value: 'is_any_of', label: 'is' },
  { value: 'is_none_of', label: 'is not' },
  ...(field === 'operation' || field === 'environment' || field === 'project'
    ? [{ value: 'matches', label: 'matches' }]
    : []),
]

const placeholders: Record<AgentPolicyCondition['field'], string> = {
  category: 'Choose kinds of action',
  operation: 'Search operations',
  environment: 'production',
  project: 'Choose projects',
  team: 'Choose teams',
  key: 'Choose keys',
}

export function ConditionRows({
  conditions,
  onChange,
  suggestions,
  errors,
}: {
  conditions: AgentPolicyCondition[]
  onChange: (conditions: AgentPolicyCondition[]) => void
  suggestions: Record<AgentPolicyCondition['field'], Suggestion[]>
  errors: Record<string, string>
}) {
  const update = (index: number, patch: Partial<AgentPolicyCondition>) =>
    onChange(conditions.map((c, i) => (i === index ? { ...c, ...patch } : c)))
  const used = new Set(conditions.map((c) => c.field))
  const unused = policyFields.filter((f) => !used.has(f))
  return (
    <div className="@container grid gap-0">
      {conditions.map((condition, index) => {
        const error = errors[`when.${index}`]
        return (
          <div key={index} className="grid">
            {index > 0 && (
              <div className="flex items-center gap-2 py-1.5 pl-3" aria-hidden>
                <span className="h-4 w-px bg-[var(--hairline)]" />
                <span className="rounded-full border border-[var(--hairline)] px-2 text-[11px] leading-4 font-medium tracking-wide text-[var(--muted-foreground)] uppercase">
                  and
                </span>
              </div>
            )}
            <div className="grid min-w-0 grid-cols-[minmax(0,1.5fr)_minmax(0,1fr)_2.5rem] gap-2 @2xl:grid-cols-[12rem_8.5rem_minmax(0,1fr)_2.5rem] @2xl:items-start">
              <SelectField
                label={`Condition ${index + 1} field`}
                value={condition.field}
                onValueChange={(field) => {
                  const next = field as AgentPolicyCondition['field']
                  update(index, {
                    field: next,
                    values: [],
                    operator:
                      condition.operator === 'matches' &&
                      !operatorOptions(next).some((o) => o.value === 'matches')
                        ? 'is_any_of'
                        : condition.operator,
                  })
                }}
                options={policyFields.map((f) => ({
                  value: f,
                  label: fieldLabels[f],
                  disabled: f !== condition.field && used.has(f),
                }))}
              />
              <SelectField
                label={`Condition ${index + 1} operator`}
                value={condition.operator}
                onValueChange={(operator) =>
                  update(index, { operator: operator as AgentPolicyCondition['operator'] })
                }
                options={operatorOptions(condition.field)}
              />
              <div className="order-last col-span-3 min-w-0 @2xl:order-none @2xl:col-span-1">
                <ValuePicker
                  label={`Condition ${index + 1} values`}
                  values={condition.values}
                  onChange={(values) => update(index, { values })}
                  options={condition.operator === 'matches' ? [] : suggestions[condition.field]}
                  freeText={
                    condition.operator === 'matches' ||
                    condition.field === 'environment' ||
                    condition.field === 'project'
                  }
                  mono={condition.field === 'operation'}
                  error={Boolean(error)}
                  placeholder={
                    condition.operator === 'matches'
                      ? condition.field === 'operation'
                        ? 'delete*'
                        : 'prod*'
                      : placeholders[condition.field]
                  }
                />
              </div>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="justify-self-end"
                aria-label={`Remove condition ${index + 1}`}
                disabled={conditions.length === 1}
                onClick={() => onChange(conditions.filter((_, i) => i !== index))}
              >
                <Icon name="x" size={16} />
              </Button>
            </div>
            {error && (
              <p className="mt-1 text-xs text-[var(--error-text)] @2xl:pl-[21rem]">{error}</p>
            )}
          </div>
        )
      })}
      {errors.when && <p className="mt-1 text-xs text-[var(--error-text)]">{errors.when}</p>}
      <div className="mt-3">
        <Menu
          align="start"
          trigger={
            <Button type="button" size="sm" disabled={!unused.length || conditions.length >= 8}>
              <Icon name="plus" size={14} />
              Add condition
            </Button>
          }
        >
          {unused.map((field) => (
            <MenuItem
              key={field}
              onSelect={() =>
                onChange([
                  ...conditions,
                  {
                    field,
                    operator: field === 'operation' ? 'matches' : 'is_any_of',
                    values: [],
                  },
                ])
              }
            >
              <span className="grid gap-0.5">
                <span>{fieldLabels[field]}</span>
                <span className="text-xs text-[var(--muted-foreground)]">{fieldHints[field]}</span>
              </span>
            </MenuItem>
          ))}
        </Menu>
      </div>
    </div>
  )
}
