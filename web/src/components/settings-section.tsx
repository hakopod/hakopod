import { useId, type ReactNode } from 'react'
import { HeadingHelp } from './shared'

// Every settings section opens the same way: what it controls, its current state in
// one line, and at most a couple of actions on the title row.
export function SettingsSection({
  title,
  state,
  help,
  actions,
  children,
  level = 2,
  className = '',
}: {
  title: string
  state?: ReactNode
  help?: ReactNode
  actions?: ReactNode
  children?: ReactNode
  level?: 2 | 3
  className?: string
}) {
  const id = useId()
  const Heading = level === 2 ? 'h2' : 'h3'
  return (
    <section
      className={`hako-settings-section settings-section-${level} ${className}`}
      aria-labelledby={id}
    >
      <header className="section-toolbar settings-section-head">
        <div className="settings-section-title">
          <div className="hako-section-heading-title">
            <Heading id={id}>{title}</Heading>
            {help && <HeadingHelp title={title}>{help}</HeadingHelp>}
          </div>
          {state && <p className="settings-section-state">{state}</p>}
        </div>
        {actions && <div className="toolbar-actions">{actions}</div>}
      </header>
      {children}
    </section>
  )
}

export function SettingsRows({ label, children }: { label: string; children: ReactNode }) {
  return (
    <ul className="settings-rows" aria-label={label}>
      {children}
    </ul>
  )
}

// Rows keep four slots in a fixed order so identities, states, metadata and actions
// line up down the list even when a slot is empty.
export function SettingsRow({
  mark,
  name,
  detail,
  status,
  meta,
  actions,
  muted = false,
  className = '',
}: {
  mark?: ReactNode
  name: ReactNode
  detail?: ReactNode
  status?: ReactNode
  meta?: ReactNode
  actions?: ReactNode
  muted?: boolean
  className?: string
}) {
  return (
    <li className={`settings-row ${muted ? 'settings-row-revoked' : ''} ${className}`}>
      <div className="settings-row-identity">
        {mark}
        <div>
          <strong>{name}</strong>
          {detail && <small>{detail}</small>}
        </div>
      </div>
      <div className="settings-row-status">{status}</div>
      <div className="settings-row-meta">{meta}</div>
      <div className="settings-row-actions">{actions}</div>
    </li>
  )
}

export function SettingsState({
  tone,
  children,
}: {
  tone: 'ok' | 'warn' | 'danger' | 'muted'
  children: ReactNode
}) {
  return <span className={`settings-state settings-state-${tone}`}>{children}</span>
}

// What is absent, one line on why it matters, and the one action that fixes it.
export function SettingsEmpty({
  title,
  children,
  action,
}: {
  title: string
  children?: ReactNode
  action?: ReactNode
}) {
  return (
    <div className="settings-empty">
      <div>
        <strong>{title}</strong>
        {children && <p>{children}</p>}
      </div>
      {action}
    </div>
  )
}
