import { createFileRoute } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { AuthScreen } from '../components/auth-screen'
import { Empty } from '../components/shared'

export const Route = createFileRoute('/login/invite')({
  validateSearch: (search: Record<string, unknown>) => ({
    token: typeof search.token === 'string' ? search.token.slice(0, 512) : '',
    workspace: search.workspace === 'personal' ? 'personal' : 'invite',
  }),
  component: Invite,
})
function Invite() {
  const { token } = Route.useSearch()
  const cache = useQueryClient()
  if (!token)
    return (
      <Empty
        title="Missing invitation"
        description="Open the invitation link shared by your team administrator."
      />
    )
  return (
    <div className="invite-in-workspace">
      <AuthScreen
        signedIn
        inviteToken={token}
        onSuccess={() => {
          cache.clear()
          window.location.assign('/')
        }}
      />
    </div>
  )
}
