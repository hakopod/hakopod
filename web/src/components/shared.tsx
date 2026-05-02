import type { ReactNode } from 'react'
import { useState } from 'react'
import { Icon } from './icons'
import { Button } from './ui/button'
import { message } from '../lib/api'

export function Status({ value, small }: { value?: string; small?: boolean }) {
  const status = (value || 'unknown').toLowerCase()
  const color = ['healthy', 'ready', 'succeeded', 'running'].includes(status)
    ? status === 'running'
      ? 'blue'
      : 'green'
    : ['failed', 'error', 'unhealthy', 'not ready'].includes(status)
      ? 'red'
      : ['queued', 'pending', 'partial', 'progressing'].includes(status)
        ? 'amber'
        : 'muted'
  return (
    <span className={`status status-${color} ${small ? 'status-small' : ''}`}>
      <span className="status-dot" />
      {status.replaceAll('_', ' ')}
    </span>
  )
}
export function Empty({
  icon = 'box',
  title,
  description,
  action,
}: {
  icon?: string
  title: string
  description: string
  action?: ReactNode
}) {
  return (
    <div className="empty-state">
      <div className="empty-icon">
        <Icon name={icon} size={26} />
      </div>
      <h3>{title}</h3>
      <p>{description}</p>
      {action}
    </div>
  )
}
export function ErrorState({ error, retry }: { error: unknown; retry?: () => void }) {
  return (
    <div className="error-state" role="alert">
      <Icon name="alert" />
      <div>
        <strong>Something needs attention</strong>
        <p>{message(error)}</p>
      </div>
      {retry && (
        <Button size="sm" onClick={retry}>
          Retry
        </Button>
      )}
    </div>
  )
}
export function Loading({ rows = 3 }: { rows?: number }) {
  return (
    <div className="loading-stack" aria-label="Loading" role="status">
      {Array.from({ length: rows }, (_, i) => (
        <div className="skeleton" key={i} />
      ))}
      <span className="sr-only">Loading data…</span>
    </div>
  )
}
export function Copy({ value, label }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <Button
      type="button"
      size={label ? 'sm' : 'icon'}
      variant="ghost"
      title="Copy to clipboard"
      aria-label={copied ? 'Copied' : 'Copy to clipboard'}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value)
          setCopied(true)
          setTimeout(() => setCopied(false), 1800)
        } catch {
          setCopied(false)
        }
      }}
    >
      <Icon name={copied ? 'check' : 'copy'} size={14} />
      {label && (copied ? 'Copied' : label)}
    </Button>
  )
}
export function PageHeader({
  eyebrow,
  title,
  description,
  action,
}: {
  eyebrow?: string
  title: string
  description?: string
  action?: ReactNode
}) {
  return (
    <div className="page-heading">
      <div>
        {eyebrow && <div className="eyebrow">{eyebrow}</div>}
        <h1>{title}</h1>
        {description && <p>{description}</p>}
      </div>
      {action && <div className="heading-action">{action}</div>}
    </div>
  )
}
export function Note({ children }: { children: ReactNode }) {
  return (
    <div className="note">
      <Icon name="info" size={16} />
      <div>{children}</div>
    </div>
  )
}
