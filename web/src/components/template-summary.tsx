import type { ReactNode } from 'react'
import type { components } from '../lib/api.generated'
import { workloadRequirementLabel } from '../lib/template-requirements'
import { Icon } from './icons'

type Template = components['schemas']['Template']

// What a template creates and needs, stated before any input is asked for.
export function TemplateSummary({
  template,
  target,
}: {
  template: Template
  /** Where the template deploys: an existing application or a project / environment. */
  target: { application?: string; project: string; environment: string }
}) {
  const needs = (template.workload_requirements || []).map(
    (item) => workloadRequirementLabel[item] || item.replaceAll('_', ' '),
  )
  const links = [
    ...template.sources,
    ...(template.upstream.startsWith('https://') && !template.sources.includes(template.upstream)
      ? [template.upstream]
      : []),
  ].filter((url) => url.startsWith('https://'))
  const rows: [string, ReactNode][] = []
  if (template.deployable) {
    rows.push([
      'Creates',
      target.application
        ? `New services in ${target.application}`
        : `A new application in ${target.project} / ${target.environment}`,
    ])
    rows.push([
      'Network',
      template.category === 'database' ? 'Private endpoint only' : 'Private unless you expose it',
    ])
    rows.push(['Resources', template.resource_summary])
  }
  if (needs.length) rows.push(['Needs', <List items={needs} />])
  // A guide-only template lists its prerequisites and verification in the main column.
  if (template.deployable && template.requirements.length)
    rows.push(['Before you deploy', <List items={template.requirements} />])
  if (template.required_secrets.length)
    rows.push([
      template.deployable ? 'Secrets you set in review' : 'Candidate credentials',
      <span className="template-summary-codes">
        {template.required_secrets.map((name) => (
          <code key={name}>{name}</code>
        ))}
      </span>,
    ])
  rows.push(['Architectures', template.architectures.join(' · ') || 'See the deployment guide'])
  if (template.deployable && template.verification)
    rows.push(['Verified', template.verification])
  if (template.license) rows.push(['License', template.license])
  return (
    <section className="template-summary" aria-label={`About ${template.name}`}>
      <dl>
        {rows.map(([term, value]) => (
          <div key={term}>
            <dt>{term}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
      {links.length > 0 && (
        <ul className="template-summary-links" aria-label="Upstream references">
          {links.map((url, index) => (
            <li key={url}>
              <a href={url} target="_blank" rel="noreferrer">
                {index ? new URL(url).hostname : 'Upstream setup guide'}
                <Icon name="external" size={12} />
              </a>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function List({ items }: { items: string[] }) {
  return (
    <ul>
      {items.map((item) => (
        <li key={item}>{item}</li>
      ))}
    </ul>
  )
}
