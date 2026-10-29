import type { ReactNode } from 'react'
import { Icon } from './icons'

// The review step of a create or change flow: the facts that will be saved, then
// the consequences in plain sentences. Styles live in networks.css.

export function ReviewFacts({
  label,
  items,
}: {
  label: string
  items: readonly (readonly [string, ReactNode])[]
}) {
  return (
    <dl className="task-facts" aria-label={label}>
      {items.map(([term, value]) => (
        <div key={term}>
          <dt>{term}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  )
}

export function Consequences({ children }: { children: ReactNode }) {
  return (
    <section className="task-consequences" aria-label="What happens next">
      <h3>What happens</h3>
      <ul>{children}</ul>
    </section>
  )
}

export function Consequence({
  tone = 'info',
  children,
}: {
  tone?: 'info' | 'warning'
  children: ReactNode
}) {
  return (
    <li data-tone={tone}>
      <Icon name={tone === 'warning' ? 'alert' : 'info'} size={14} />
      <span>{children}</span>
    </li>
  )
}

// Where a resource lives. Shown on every list and flow so scope is never implied.
export function ScopeLabel({ project, environment }: { project: string; environment: string }) {
  return (
    <span className="task-scope" title={`Project ${project}, environment ${environment}`}>
      <Icon name="grid" size={13} />
      <span>
        {project} <span aria-hidden="true">/</span>
        <span className="sr-only">, environment</span> {environment}
      </span>
    </span>
  )
}
