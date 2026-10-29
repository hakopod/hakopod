import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Button } from './ui/button'
import { Icon } from './icons'

// Dialog widths from the design system: one short task, or a consequential review.
export const focusedDialog = 'focused-dialog'
export const reviewDialog = 'review-dialog'

type Fact = readonly [term: string, value: ReactNode]

/** Scope, target, affected resources and consequences, shown before the action. */
export function ReviewFacts({
  facts,
  label = 'Review',
}: {
  facts: ReadonlyArray<Fact | false | null | undefined | ''>
  label?: string
}) {
  return (
    <dl className="review-facts" aria-label={label}>
      {facts
        .filter((fact): fact is Fact => Boolean(fact))
        .map(([term, value]) => (
          <div key={term}>
            <dt>{term}</dt>
            <dd>{value}</dd>
          </div>
        ))}
    </dl>
  )
}

/** A consequence or requirement that must stay visible where the decision is made. */
export function ReviewNotice({
  tone = 'info',
  title,
  children,
}: {
  tone?: 'info' | 'warn' | 'danger'
  title: string
  children?: ReactNode
}) {
  return (
    <div className={`review-notice review-notice-${tone}`}>
      <Icon name={tone === 'info' ? 'info' : 'alert'} size={16} />
      <div>
        <strong>{title}</strong>
        {children && <div className="review-notice-body">{children}</div>}
      </div>
    </div>
  )
}

/** Replaces the footer when Escape or the close button would drop something the user needs. */
export function DiscardPrompt({
  children,
  keepLabel = 'Keep editing',
  discardLabel = 'Discard draft',
  onKeep,
  onDiscard,
}: {
  children: ReactNode
  keepLabel?: string
  discardLabel?: string
  onKeep: () => void
  onDiscard: () => void
}) {
  const keep = useRef<HTMLButtonElement>(null)
  useEffect(() => keep.current?.focus(), [])
  return (
    <div className="dialog-footer review-discard" role="alert">
      <p>{children}</p>
      <div className="review-discard-actions">
        <Button ref={keep} type="button" onClick={onKeep}>
          {keepLabel}
        </Button>
        <Button type="button" variant="danger" onClick={onDiscard}>
          {discardLabel}
        </Button>
      </div>
    </div>
  )
}

/** Copy control that says which value it copied, for screen readers as well as sight. */
export function CopyValue({ value, what }: { value: string; what: string }) {
  const [copied, setCopied] = useState(false)
  const timeout = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => () => clearTimeout(timeout.current), [])
  return (
    <span className="copy-value">
      <Button
        type="button"
        size="sm"
        variant="ghost"
        aria-label={`Copy ${what}`}
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(value)
            clearTimeout(timeout.current)
            setCopied(true)
            timeout.current = setTimeout(() => setCopied(false), 2400)
          } catch {
            setCopied(false)
          }
        }}
      >
        <Icon name={copied ? 'check' : 'copy'} size={14} />
        {copied ? 'Copied' : 'Copy'}
      </Button>
      <span className="copy-value-status" role="status">
        {copied ? `Copied ${what}` : ''}
      </span>
    </span>
  )
}

export function plural(count: number, noun: string, many = `${noun}s`) {
  return `${count} ${count === 1 ? noun : many}`
}
