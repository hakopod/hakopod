import { createRouter } from '@tanstack/react-router'
import type {} from '@tanstack/react-start'
import { routeTree } from './routeTree.gen'
export function getRouter() {
  return createRouter({ routeTree, scrollRestoration: true, defaultPreload: false })
}
declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof getRouter>
  }
}
