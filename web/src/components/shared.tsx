import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { Skeleton } from '@hakopod/hatch-ui/components/skeleton'
import { Status as HatchStatus, type StatusTone } from '@hakopod/hatch-ui/components/status'
import { Icon } from './icons'
import { Button } from './ui/button'
import { Tooltip } from './ui/surfaces'
import { APIError, message } from '../lib/api'

export function Status({ value, small }: { value?: string; small?: boolean }) {
  const status = (value || 'unknown').toLowerCase().replaceAll('_', ' ')
  const tone: StatusTone = [
    'healthy',
    'ready',
    'succeeded',
    'complete',
    'completed',
    'active',
    'verified',
    'synchronized',
    'live',
  ].includes(status)
    ? 'success'
    : [
          'failed',
          'error',
          'unhealthy',
          'not ready',
          'crashed',
          'crashloop',
          'expired',
          'revoked',
          'misconfigured',
        ].includes(status)
      ? 'error'
      : ['running', 'building', 'deploying', 'progressing', 'terminating', 'issuing cert'].includes(
            status,
          )
        ? 'running'
        : [
              'pending',
              'partial',
              'degraded',
              'blocked',
              'stale',
              'expiring',
              'pending dns',
              'incomplete',
            ].includes(status)
          ? 'warning'
          : 'neutral'
  return (
    <HatchStatus tone={tone} className={`hako-status ${small ? 'hako-status-small' : ''}`}>
      {status.charAt(0).toUpperCase() + status.slice(1)}
    </HatchStatus>
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
    <div className="hako-empty-canvas bg-grid">
      <section className="hako-empty-card">
        <Brackets bold />
        <Icon name={icon} size={24} />
        <h2>{title}</h2>
        <p>{description}</p>
        {action && <div className="hako-empty-actions">{action}</div>}
      </section>
    </div>
  )
}
export function ErrorState({
  error,
  retry,
  title = 'Something needs attention',
}: {
  error: unknown
  retry?: () => void
  title?: string
}) {
  return (
    <div className="hako-error-state" role="alert" aria-atomic="true">
      <Icon name="alert" />
      <div>
        <h2>{title}</h2>
        <p>{message(error)}</p>
        {error instanceof APIError && error.code && (
          <details className="mt-2 text-xs">
            <summary>Error details</summary>
            <code>{error.code}</code>
          </details>
        )}
        {/readiness probe image|persistent storage is unavailable|maintenance service is unavailable/.test(
          message(error),
        ) && (
          <Button asChild size="sm">
            <a href="/infrastructure?tab=setup">Open infrastructure setup</a>
          </Button>
        )}
      </div>
      {retry && (
        <Button variant="primary" size="sm" onClick={retry}>
          <Icon name="refresh" size={14} />
          Retry
        </Button>
      )}
    </div>
  )
}
export function Loading({ rows = 3 }: { rows?: number }) {
  return (
    <div className="hako-loading-stack" aria-label="Loading" role="status">
      {Array.from({ length: Math.max(1, Math.min(20, rows)) }, (_, i) => (
        <Skeleton className="hako-skeleton-row" key={i} />
      ))}
      <span className="sr-only">Loading data…</span>
    </div>
  )
}
export function Copy({ value, label }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  const timeout = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => () => clearTimeout(timeout.current), [])
  return (
    <Button
      type="button"
      size={label ? 'sm' : 'icon'}
      variant="ghost"
      title="Copy to clipboard"
      aria-label={copied ? 'Copied' : label || 'Copy to clipboard'}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value)
          clearTimeout(timeout.current)
          setCopied(true)
          timeout.current = setTimeout(() => setCopied(false), 1800)
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
export function HeadingHelp({ title, children }: { title: string; children: ReactNode }) {
  const [open, setOpen] = useState(false)
  const activationOpen = useRef<boolean | null>(null)
  return (
    <Tooltip content={children} side="bottom" open={open} onOpenChange={setOpen}>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="hako-heading-help"
        aria-label={`About ${title}`}
        onPointerDownCapture={() => {
          activationOpen.current = open
        }}
        onPointerDown={(event) => event.preventDefault()}
        onKeyDown={(event) => {
          if (event.key === 'Enter' || event.key === ' ') activationOpen.current = open
        }}
        onClick={(event) => {
          event.preventDefault()
          setOpen(!(activationOpen.current ?? open))
          activationOpen.current = null
        }}
      >
        <Icon name="info" size={14} />
      </Button>
    </Tooltip>
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
    <header className="page-heading hako-page-heading">
      <div className="hako-page-heading-title">
        {eyebrow && <span className="sr-only">{eyebrow}</span>}
        <h1 className="min-w-0 break-words">{title}</h1>
        {description && <HeadingHelp title={title}>{description}</HeadingHelp>}
      </div>
      {action && <div className="heading-action">{action}</div>}
    </header>
  )
}
export function Note({ children }: { children: ReactNode }) {
  return (
    <div className="note hako-note">
      <Icon name="info" size={16} />
      <div className="min-w-0 [overflow-wrap:anywhere]">{children}</div>
    </div>
  )
}
