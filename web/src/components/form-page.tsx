import { useId, type ReactNode } from 'react'
import { Card } from '@hakopod/hatch-ui/components/card'
import { HeadingHelp, PageHeader } from './shared'

export function FormPage({
  title,
  description,
  children,
  help,
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
      <PageHeader title={title} description={description} />
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
        <h2 id={id}>{title}</h2>
        {description && <HeadingHelp title={title}>{description}</HeadingHelp>}
      </header>
      <div className="form-card-content">{children}</div>
    </Card>
  )
}

export function FormHint({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="form-hint hako-form-hint">
      <h3>{title}</h3>
      <div>{children}</div>
    </section>
  )
}
