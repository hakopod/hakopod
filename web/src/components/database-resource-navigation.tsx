import { Link } from '@tanstack/react-router'
import { Button } from './ui/button'

export function DatabaseResourceNavigation({
  active,
  scope,
}: {
  active: 'databases' | 'platforms'
  scope: { project: string; environment: string }
}) {
  return (
    <nav className="tab-list" aria-label="Database resources">
      <Button asChild variant="ghost" className="tab-trigger">
        <Link
          to="/databases"
          search={scope}
          aria-current={active === 'databases' ? 'page' : undefined}
        >
          Databases
        </Link>
      </Button>
      <Button asChild variant="ghost" className="tab-trigger">
        <Link
          to="/platforms"
          search={scope}
          aria-current={active === 'platforms' ? 'page' : undefined}
        >
          Platforms
        </Link>
      </Button>
    </nav>
  )
}
