import { Link } from '@tanstack/react-router'
import type { Application } from '../lib/types'
import { HeadingHelp } from './shared'
import { Button } from './ui/button'

export function ManagedDatabaseConnections({
  application,
  service,
}: {
  application: Application
  service?: string
}) {
  const bindings = Object.entries(application.spec.services).flatMap(([name, svc]) =>
    service && name !== service
      ? []
      : Object.entries(svc.bindings || {})
          .filter(([, b]) => b.managed_database)
          .map(([variable, b]) => ({ service: name, variable, binding: b })),
  )
  if (!bindings.length) return null
  return (
    <section className="grid gap-3 py-4" aria-label="Managed database connections">
      <div className="flex items-center gap-2">
        <h2>Managed database connections</h2>
        <HeadingHelp title="Managed database connections">
          Saved references resolve to private database credentials at deployment. Database replicas
          have their own controller and resources.
        </HeadingHelp>
      </div>
      <ul className="grid gap-3">
        {bindings.map(({ service: name, variable, binding }) => (
          <li key={`${name}/${variable}`} className="flex flex-wrap items-center gap-2">
            <code className="break-all">
              {name} / {variable}
            </code>
            <span>{binding.endpoint?.replaceAll('_', ' ')}</span>
            <Button asChild size="sm">
              <Link
                to="/databases/$databaseId"
                params={{ databaseId: binding.managed_database! }}
                search={{ project: application.project, environment: application.environment }}
              >
                Database {binding.managed_database!.slice(0, 8)}
              </Link>
            </Button>
          </li>
        ))}
      </ul>
    </section>
  )
}
