import { Link } from '@tanstack/react-router'
import type { Application } from '../lib/types'
import { HeadingHelp } from './shared'
import { Button } from './ui/button'
import { databaseBindingSummary } from './database-binding-options'
import { BindingConnectionTest } from './binding-connection-test'

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
          .filter(
            ([, b]) =>
              b.managed_database ||
              b.external_database ||
              ['postgres', 'mysql', 'redis', 'mongodb', 'clickhouse', 'oracle'].includes(
                b.protocol,
              ),
          )
          .map(([variable, b]) => ({ service: name, variable, binding: b })),
  )
  if (!bindings.length) return null
  return (
    <section className="grid gap-3 py-4" aria-label="Database connections">
      <div className="flex items-center gap-2">
        <h2>Database connections</h2>
        <HeadingHelp title="Database connections">
          Bindings supply private connection values to this service. Test connection checks one
          running application pod using its container environment. PostgreSQL, Redis and MySQL
          checks verify the connection, authentication and a minimal read-only query. TLS checks
          verify the hostname and certificate chain; bindings configured without TLS show that
          explicitly. Results identify the tested pod; other replicas and application table
          permissions need their own checks.
        </HeadingHelp>
      </div>
      <ul className="grid gap-3">
        {bindings.map(({ service: name, variable, binding }) => (
          <li key={`${name}/${variable}`} className="flex min-w-0 flex-wrap items-center gap-2">
            <code className="break-all">
              {name} / {variable}
            </code>
            <span>{binding.endpoint?.replaceAll('_', ' ') || binding.protocol}</span>
            {binding.managed_database && (
              <Button asChild size="sm">
                <Link
                  to="/databases/$databaseId"
                  params={{ databaseId: binding.managed_database! }}
                  search={{ project: application.project, environment: application.environment }}
                >
                  Database {binding.managed_database!.slice(0, 8)}
                </Link>
              </Button>
            )}
            <dl className="flex min-w-0 basis-full flex-wrap gap-x-4 gap-y-1 text-xs">
              {databaseBindingSummary(binding).map(([label, value]) => (
                <div key={label} className="flex min-w-0 gap-1">
                  <dt className="text-muted-foreground">{label}:</dt>
                  <dd className="min-w-0 break-all">{value}</dd>
                </div>
              ))}
            </dl>
            <BindingConnectionTest
              key={`${application.id}/${name}/${variable}/${application.revision}`}
              application={application}
              service={name}
              variable={variable}
            />
          </li>
        ))}
      </ul>
    </section>
  )
}
