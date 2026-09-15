import { createRouter } from '@tanstack/react-router'
import type {} from '@tanstack/react-start'
import { routeTree } from './routeTree.gen'
import { ErrorState } from './components/shared'
import { APIError } from './lib/api'
export function getRouter() {
  return createRouter({
    routeTree,
    scrollRestoration: true,
    defaultPreload: false,
    defaultErrorComponent: ({ error, reset }) => (
      <div className="ops-page">
        <ErrorState
          title="This page could not load"
          error={
            error instanceof APIError
              ? error
              : 'Try loading the page again. If you just submitted a change, check its status before retrying it.'
          }
          retry={reset}
        />
      </div>
    ),
  })
}
declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof getRouter>
  }
}
