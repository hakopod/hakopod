import { dashboardEdition } from '../lib/dashboard-edition'
import { useState } from 'react'
import { createRootRoute, HeadContent, Outlet, Scripts } from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { DashboardShell } from '../components/shell'
import { APIError } from '../lib/api'
import { Empty } from '../components/shared'
import { Button } from '../components/ui/button'
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
      { rel: 'icon', type: 'image/x-icon', href: '/favicon.ico?v=hakopod-1' },
      { rel: 'icon', type: 'image/png', sizes: '32x32', href: '/favicon-32.png?v=hakopod-1' },
      { rel: 'icon', type: 'image/svg+xml', sizes: 'any', href: '/favicon.svg?v=hakopod-1' },
      { rel: 'apple-touch-icon', sizes: '180x180', href: '/apple-touch-icon.png?v=hakopod-1' },
    ],
  }),
  component: Root,
  notFoundComponent: () => (
    <Empty
      title="Page not found"
      description="This address doesn’t match a page in this console."
      action={
        <Button variant="primary" asChild>
          <a href="/">Return to all projects</a>
        </Button>
      }
    />
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
    <html
      lang="en"
      className="dark"
      data-theme="dark"
      data-edition={dashboardEdition.cloud ? 'cloud' : 'self-hosted'}
      suppressHydrationWarning
    >
      <head>
        <HeadContent />
        <script
          dangerouslySetInnerHTML={{
            __html:
              "(()=>{let t='dark';try{if(localStorage.getItem('hakopod-theme')==='light')t='light'}catch{}const r=document.documentElement;r.dataset.theme=t;r.classList.remove('dark','light');r.classList.add(t)})()",
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
