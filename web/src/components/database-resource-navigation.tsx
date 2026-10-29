import { Link } from '@tanstack/react-router'

export function DatabaseResourceNavigation({
  active,
  scope,
}: {
  active: 'databases' | 'platforms'
  scope: { project: string; environment: string }
}) {
  return (
    <nav className="tab-list db-resource-tabs" aria-label="Database resources">
      <Link
        className="tab-trigger"
        to="/databases"
        search={scope}
        aria-current={active === 'databases' ? 'page' : undefined}
      >
        Databases
      </Link>
      <Link
        className="tab-trigger"
        to="/platforms"
        search={scope}
        aria-current={active === 'platforms' ? 'page' : undefined}
      >
        Platforms
      </Link>
    </nav>
  )
}
