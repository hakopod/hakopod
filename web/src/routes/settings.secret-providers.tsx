import { createFileRoute, Navigate, Outlet, useLocation } from '@tanstack/react-router'

export const Route = createFileRoute('/settings/secret-providers')({
  validateSearch: (search: Record<string, unknown>): { saved?: string } => ({
    saved:
      typeof search.saved === 'string' && /^[a-z][a-z0-9-]{0,39}$/.test(search.saved)
        ? search.saved
        : undefined,
  }),
  component: ProvidersRoute,
})
function ProvidersRoute() {
  const path = useLocation().pathname
  const { saved } = Route.useSearch()
  if (path !== '/settings/secret-providers') return <Outlet />
  // The provider list lives in the settings hub; task pages return there.
  return <Navigate to="/settings" search={{ tab: 'secret-providers', saved }} replace />
}
