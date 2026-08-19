import { useState } from 'react'
import { createRootRoute, HeadContent, Outlet, Scripts } from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { DashboardShell } from '../components/shell'
import { APIError } from '../lib/api'
import css from '../styles.css?url'

export const Route = createRootRoute({
  head: () => ({
    meta: [
      { charSet: 'utf-8' },
      { name: 'viewport', content: 'width=device-width, initial-scale=1' },
      { title: 'Hakopod · Your apps. Your rules.' },
      {
        name: 'description',
        content: 'Deploy and operate container applications on infrastructure you own.',
      },
      { name: 'referrer', content: 'no-referrer' },
    ],
    links: [
      { rel: 'stylesheet', href: css },
      { rel: 'icon', type: 'image/svg+xml', href: '/favicon.svg' },
    ],
  }),
  component: Root,
  notFoundComponent: () => (
    <div className="empty-state">
      <h1>Page not found</h1>
      <a href="/">Return to applications</a>
    </div>
  ),
})

function Root() {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            staleTime: 15000,
            gcTime: 60000,
            refetchOnWindowFocus: true,
            refetchIntervalInBackground: false,
            retry: (count, error) =>
              !(error instanceof APIError && error.status < 500) && count < 1,
          },
        },
      }),
  )
  return (
    <html lang="en" suppressHydrationWarning>
      <head>
        <HeadContent />
        <script
          dangerouslySetInnerHTML={{
            __html:
              "try{document.documentElement.dataset.theme=localStorage.getItem('hakopod-theme')||'dark'}catch{}",
          }}
        />
      </head>
      <body>
        <QueryClientProvider client={queryClient}>
          <DashboardShell>
            <Outlet />
          </DashboardShell>
        </QueryClientProvider>
        <Scripts />
      </body>
    </html>
  )
}
