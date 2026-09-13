import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { submitSession } from '../components/auth-screen'
import { Button } from '../components/ui/button'
import { PageHeader, Empty, ErrorState, Loading } from '../components/shared'

export const Route = createFileRoute('/login/onboarding')({ component: Onboarding })

function Onboarding() {
  const cache = useQueryClient()
  const state = useQuery({
    queryKey: ['account-onboarding'],
    queryFn: ({ signal }) => unwrap(client.GET('/auth/onboarding', { signal })),
    retry: false,
  })
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  async function choose(choice: 'personal' | 'invite', invite_id = '') {
    if (busy) return
    setBusy(invite_id || choice)
    setError('')
    try {
      await submitSession({ action: 'onboarding', choice, invite_id })
      cache.clear()
      window.location.assign('/')
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy('')
    }
  }
  if (state.isPending) return <Loading rows={3} />
  if (state.error) return <ErrorState error={state.error} retry={() => void state.refetch()} />
  if (!state.data.required)
    return (
      <Empty
        title="Your account is ready"
        description="You can return to your applications."
        action={
          <Button variant="primary" asChild>
            <a href="/">Open dashboard</a>
          </Button>
        }
      />
    )
  return (
    <section className="onboarding-page">
      <PageHeader
        title="Choose your workspace"
        description="Join a team that invited you, or start in a private workspace of your own."
      />
      {error && (
        <p role="alert" className="inline-error">
          {error}
        </p>
      )}
      <div className="onboarding-options">
        {state.data.invitations.map((invite) => (
          <article className="panel onboarding-option" key={invite.id}>
            <span className="eyebrow">You are invited</span>
            <h2>{invite.team_name || invite.project}</h2>
            <p>
              {invite.project ? `Project ${invite.project} · ` : ''}
              {invite.role} access. Only this invitation’s permissions will be added.
            </p>
            <Button
              variant="primary"
              disabled={Boolean(busy)}
              onClick={() => void choose('invite', invite.id)}
            >
              {busy === invite.id ? 'Joining…' : 'Join workspace'}
            </Button>
          </article>
        ))}
        <article className="panel onboarding-option">
          <span className="eyebrow">Your own space</span>
          <h2>Personal workspace</h2>
          <p>
            A private project with a development environment. Your deployments stay separate from
            the inviting team.
          </p>
          <Button
            variant={state.data.invitations.length ? 'outline' : 'primary'}
            disabled={Boolean(busy)}
            onClick={() => void choose('personal')}
          >
            {busy === 'personal' ? 'Creating…' : 'Create personal workspace'}
          </Button>
        </article>
      </div>
    </section>
  )
}
