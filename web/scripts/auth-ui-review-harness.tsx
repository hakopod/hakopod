// Development-only visual-review entrypoint for the real authentication screen.
// It is not imported by product routes. Open, for example:
// /scripts/auth-ui-review.html?view=signup&theme=paper&setup=0
import '../src/styles.css'
import { useState } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthScreen } from '../src/components/auth-screen'

const query = new URLSearchParams(location.search)
const theme = query.get('theme') === 'paper' ? 'light' : 'dark'
document.documentElement.dataset.theme = theme
document.documentElement.dataset.edition = 'self-hosted'
document.documentElement.className = theme

// AuthScreen reads its view from /login/<view>; keep the fixture query.
history.replaceState(null, '', `/login/${query.get('view') || 'login'}${location.search}`)

const status = {
  setup_required: query.get('setup') === '1',
  password: true,
  providers: ['google', 'github', 'gitlab'],
  passkeys: true,
  totp: true,
  email_delivery: query.get('email') !== '0',
  signup_enabled: true,
  password_recovery: true,
  deployment_mode: 'self-hosted',
}

const originalFetch = window.fetch.bind(window)
window.fetch = async (input, init) => {
  const url = new URL(input instanceof Request ? input.url : String(input), location.origin)
  if (url.pathname === '/api/auth/status') return Response.json(status)
  if (url.pathname.startsWith('/api/'))
    return Response.json(
      { error: { code: 'fixture', message: 'Fixture: no request is sent.' } },
      { status: 400 },
    )
  return originalFetch(input, init)
}

function Fixture() {
  const [current, setCurrent] = useState(theme)
  return (
    <AuthScreen
      onSuccess={() => undefined}
      theme={current}
      toggleTheme={() => {
        const next = current === 'dark' ? 'light' : 'dark'
        document.documentElement.className = next
        document.documentElement.dataset.theme = next
        setCurrent(next)
      }}
    />
  )
}

createRoot(document.getElementById('root')!).render(
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <Fixture />
  </QueryClientProvider>,
)
