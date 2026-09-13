import { useId, type ReactNode } from 'react'
import { Card } from '@hakopod/hatch-ui/components/card'
import { Icon } from './icons'

export function FormPage({
  title,
  description,
  children,
  help,
  icon = 'settings',
}: {
  title: string
  description: string
  breadcrumbs: { label: string; to?: string }[]
  children: ReactNode
  help?: ReactNode
  icon?: string
}) {
  return (
    <div className="form-page hako-form-page">
      <header className="form-page-heading">
        <div className="form-page-symbol">
          <Icon name={icon} size={24} />
        </div>
        <div>
          <h1>{title}</h1>
          <p>{description}</p>
        </div>
      </header>
      <div className={`form-page-layout ${help ? 'with-help' : ''}`}>
        <div className="form-page-main">{children}</div>
        {help && <aside className="form-page-help">{help}</aside>}
      </div>
    </div>
  )
}

export function FormSection({
  title,
  description,
  children,
  icon = 'settings',
}: {
  title: string
  description?: string
  children: ReactNode
  icon?: string
}) {
  const id = useId()
  return (
    <Card className="form-card hako-form-section" role="region" aria-labelledby={id}>
      <header>
        <Icon name={icon} size={17} />
        <div>
          <h2 id={id}>{title}</h2>
          {description && <p>{description}</p>}
        </div>
      </header>
      <div className="form-card-content">{children}</div>
    </Card>
  )
}

export function FormHint({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="form-hint hako-form-hint">
      <Icon name="info" size={17} />
      <h3>{title}</h3>
      <div>{children}</div>
    </section>
  )
}
