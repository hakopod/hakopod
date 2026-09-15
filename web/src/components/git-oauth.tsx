import { useEffect, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope } from '../lib/scope'
import { useGitProviderSetup, type GitConnection } from '../lib/git-connections'
import { Button } from './ui/button'
import { Copy, Empty, ErrorState, Loading, Note } from './shared'
import { FormPage, FormSection } from './form-page'

export function GitWebhookAddress({ path }: { path: string }) {
  const query = useGitProviderSetup()
  if (query.isPending) return <Loading />
  if (query.error) return <ErrorState error={query.error} />
  if (!query.data.public_url)
    return (
      <Note>
        Configure a publicly reachable HTTPS dashboard URL before adding provider webhooks.{' '}
        <Link
          className="underline! underline-offset-2"
          to="/infrastructure"
          search={{ tab: 'setup' }}
        >
          Open installation setup
        </Link>
        .
      </Note>
    )
  const address = `${query.data.public_url}${path}`
  return (
    <div className="flex min-w-0 items-center gap-3">
      <code className="break-all">{address}</code>
      <Copy value={address} />
    </div>
  )
}
export function GitOAuthAuthorize({ connection }: { connection: GitConnection }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <div className="grid gap-3">
      <p className="field-help">
        GitLab OAuth uses the authorizing user’s repository access. It does not create a
        repository-scoped app installation. Save any edits before authorizing.
      </p>
      <Button
        type="button"
        disabled={busy || !connection.enabled}
        onClick={async () => {
          if (busy) return
          setBusy(true)
          setError('')
          try {
            const result = await unwrap(
              client.POST('/git/connections/{id}/authorize', {
                params: { path: { id: connection.id } },
                body: {},
              }),
            )
            const target = new URL(result.authorization_url)
            if (
              target.origin !== 'https://gitlab.com' ||
              target.pathname !== '/oauth/authorize' ||
              target.username ||
              target.password
            )
              throw new Error('GitLab returned an unsupported authorization address.')
            window.location.assign(target.href)
          } catch (cause) {
            setError(message(cause))
            setBusy(false)
          }
        }}
      >
        {busy
          ? 'Opening GitLab…'
          : connection.configured
            ? 'Reconnect GitLab account'
            : 'Authorize GitLab account'}
      </Button>
      {error && <ErrorState error={error} />}
    </div>
  )
}
export function GitOAuthCallback() {
  const scope = useScope()
  const cache = useQueryClient()
  const started = useRef(false)
  const [result, setResult] = useState<GitConnection | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    if (!(scope.identity.admin || scope.identity.can_manage_git) || started.current) return
    started.current = true
    const params = new URLSearchParams(window.location.search)
    const code = params.get('code') || ''
    const state = params.get('state') || ''
    window.history.replaceState(window.history.state, '', window.location.pathname)
    if (params.has('error')) {
      setError('GitLab authorization was cancelled. Return to the connection to try again.')
      return
    }
    if (!code || !state || code.length > 4096 || state.length > 512) {
      setError('The authorization response is incomplete. Restart from your Git connection.')
      return
    }
    void unwrap(client.POST('/git/oauth/complete', { body: { code, state } }))
      .then((connection) => {
        setResult(connection)
        void cache.invalidateQueries({ queryKey: ['git-connections'] })
        void cache.invalidateQueries({ queryKey: ['git-connection', connection.id] })
      })
      .catch((cause) => setError(message(cause)))
  }, [scope.identity.admin || scope.identity.can_manage_git, cache])
  if (!(scope.identity.admin || scope.identity.can_manage_git))
    return (
      <Empty
        title="Repository management access required"
        description="Complete repository authorization in the same browser session that started it."
      />
    )
  return (
    <FormPage
      title="GitLab authorization"
      description="Verify the GitLab account for this repository connection."
      breadcrumbs={[]}
    >
      <FormSection
        title={
          result
            ? 'Account connected'
            : error
              ? 'Authorization needs attention'
              : 'Verifying authorization'
        }
      >
        {result ? (
          <>
            <p>
              {result.account} can now be used by {result.name}.
            </p>
            <Note>
              For automatic deployments, configure the repository webhook with this URL and its
              saved secret, then enable automatic deployment on the application source.
            </Note>
            <GitWebhookAddress path={result.webhook_path} />
            <Button asChild>
              <Link
                to="/settings/git/connections/$connectionId"
                params={{ connectionId: result.id }}
              >
                Open connection
              </Link>
            </Button>
          </>
        ) : error ? (
          <ErrorState error={error} />
        ) : (
          <Loading />
        )}
      </FormSection>
      <div className="form-footer">
        <Button asChild>
          <Link to="/settings" search={{ tab: 'github' }}>
            Back to Git connections
          </Link>
        </Button>
      </div>
    </FormPage>
  )
}
