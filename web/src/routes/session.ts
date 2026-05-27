import { createFileRoute } from '@tanstack/react-router'
import { pendingMFA, signIn, signOut } from '../server/auth'
import { privateHeaders } from '../server/session'

export const Route = createFileRoute('/session')({
  server: {
    handlers: {
      GET: ({ request }) =>
        Response.json({ mfa_required: pendingMFA(request) }, { headers: privateHeaders }),
      POST: ({ request }) => signIn(request),
      DELETE: ({ request }) => signOut(request),
    },
  },
})
