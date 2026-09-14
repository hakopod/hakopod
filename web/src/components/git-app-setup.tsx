import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope } from '../lib/scope'
import { useGitProviderSetup } from '../lib/git-connections'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { FormSection } from './form-page'
import { Copy, ErrorState, Loading, Note } from './shared'
import { FormPage } from './form-page'

type Setup = components['schemas']['GitAppSetup']

// Provider destinations are fixed, never arbitrary redirects from query params.
export function gitAppDestination(setup: Setup) {
  const u = new URL(setup.action_url)
  const path =
    setup.phase === 'manifest'
      ? /^\/(?:settings\/apps\/new|organizations\/[A-Za-z0-9-]+\/settings\/apps\/new)$/
      : /^\/apps\/[A-Za-z0-9-]+\/installations\/new$/
  if (
    u.origin !== 'https://github.com' ||
    u.username ||
    u.password ||
    !path.test(u.pathname) ||
    !u.searchParams.get('state')
  )
    throw new Error('Unsupported GitHub setup address.')
  return u.href
}
export function openGitAppSetup(setup: Setup) {
  const destination = gitAppDestination(setup)
  if (setup.phase === 'install') {
    window.location.assign(destination)
    return
  }
  if (!setup.manifest) throw new Error('GitHub App manifest is missing.')
  const form = document.createElement('form')
  form.method = 'post'
  form.action = destination
  const input = document.createElement('input')
  input.type = 'hidden'
  input.name = 'manifest'
  input.value = setup.manifest
  form.append(input)
  document.body.append(form)
  form.submit()
  form.remove()
}
export function GitLabRegistrationCallback() {
  const query = useGitProviderSetup()
  if (query.isPending) return <Loading />
  if (query.error) return <ErrorState error={query.error} />
  if (!query.data.gitlab_callback_url)
    return (
      <Note>
        {query.data.gitlab_notice}{' '}
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
  return (
    <div className="flex min-w-0 items-center gap-3">
      <code className="break-all">{query.data.gitlab_callback_url}</code>
      <Copy value={query.data.gitlab_callback_url} />
    </div>
  )
}
export function GitHubAppSetup({ id, footerActions }: { id?: string; footerActions?: ReactNode }) {
  const cache = useQueryClient()
  const providerSetup = useGitProviderSetup()
  const [name, setName] = useState('')
  const [organization, setOrganization] = useState('')
  const [builds, setBuilds] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [savedID, setSavedID] = useState(id)
  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault()
        if (busy) return
        setBusy(true)
        setError('')
        try {
          const setup = savedID
            ? await unwrap(
                client.POST('/git/connections/{id}/github/setup', {
                  params: { path: { id: savedID } },
                  body: {},
                }),
              )
            : await unwrap(
                client.POST('/git/github/start', {
                  body: { name: name.trim(), organization: organization.trim(), builds },
                }),
              )
          setSavedID(setup.connection_id)
          void cache.invalidateQueries({ queryKey: ['git-connections'] })
          openGitAppSetup(setup)
        } catch (cause) {
          setError(message(cause))
          setBusy(false)
        }
      }}
    >
      <FormSection title={id ? 'Finish GitHub setup' : 'GitHub App'}>
        {!id && (
          <>
            <label>
              Connection name
              <Input
                required
                maxLength={80}
                value={name}
                disabled={!!savedID}
                onChange={(e) => setName(e.target.value)}
                placeholder="Engineering repositories"
              />
            </label>
            <label>
              Organization <span className="field-help">(optional)</span>
              <Input
                maxLength={39}
                value={organization}
                disabled={!!savedID}
                pattern="[A-Za-z0-9][A-Za-z0-9-]*"
                onChange={(e) => setOrganization(e.target.value)}
                placeholder="Leave empty for your personal account"
              />
            </label>
            <label className="checkbox-label">
              <Input
                type="checkbox"
                checked={builds}
                disabled={!!savedID}
                onChange={(e) => setBuilds(e.target.checked)}
              />
              Enable GitHub Actions builds
            </label>
          </>
        )}
        <p className="field-help">
          Your account or organization owns the App. GitHub will ask you to create it and choose
          which repositories it can access. Hakopod configures the callback, webhook and encrypted
          credentials.
        </p>
        {!id && (
          <Note>
            {builds
              ? 'Builds request write access to repository contents, workflows and Actions. Hakopod only installs a build workflow after you review it.'
              : 'Source-only access reads repository contents and receives push events.'}
          </Note>
        )}
        <p className="field-help">
          Requires a publicly reachable HTTPS dashboard.{' '}
          <Link
            className="underline! underline-offset-2"
            to="/infrastructure"
            search={{ tab: 'setup' }}
          >
            Check installation setup
          </Link>
          .
        </p>
        {providerSetup.isPending && <Loading />}
        {providerSetup.error && <ErrorState error={providerSetup.error} />}
        {providerSetup.data && !providerSetup.data.github_available && (
          <Note>{providerSetup.data.github_notice}</Note>
        )}
        {error && <ErrorState error={error} />}
      </FormSection>
      <div className="form-footer">
        {footerActions}
        <Button
          type="submit"
          variant="primary"
          disabled={busy || !providerSetup.data?.github_available}
        >
          {busy ? 'Opening GitHub…' : id || savedID ? 'Continue on GitHub' : 'Install GitHub App'}
        </Button>
      </div>
    </form>
  )
}
export function GitHubAppCallback({ installed = false }: { installed?: boolean }) {
  const scope = useScope()
  const cache = useQueryClient()
  const started = useRef(false)
  const [result, setResult] = useState<components['schemas']['GitConnection'] | null>(null)
  const [setup, setSetup] = useState<Setup | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    if (!scope.identity.admin || started.current) return
    started.current = true
    const params = new URLSearchParams(window.location.search)
    window.history.replaceState(window.history.state, '', window.location.pathname)
    const state = params.get('state') || ''
    const code = params.get('code') || ''
    const installationID = Number(params.get('installation_id'))
    if (
      params.has('error') ||
      !state ||
      (installed ? !Number.isSafeInteger(installationID) || installationID < 1 : !code)
    ) {
      setError(
        'Setup was cancelled or the response is incomplete. Reopen your saved Git connection to continue.',
      )
      return
    }
    if (installed) {
      void unwrap(
        client.POST('/git/github/install/complete', {
          body: { state, installation_id: installationID },
        }),
      )
        .then((c) => {
          setResult(c)
          void cache.invalidateQueries({ queryKey: ['git-connections'] })
          void cache.invalidateQueries({ queryKey: ['git-connection', c.id] })
        })
        .catch((cause) => setError(message(cause)))
    } else {
      void unwrap(client.POST('/git/github/complete', { body: { state, code } }))
        .then((next) => {
          gitAppDestination(next)
          setSetup(next)
          void cache.invalidateQueries({ queryKey: ['git-connections'] })
        })
        .catch((cause) => setError(message(cause)))
    }
  }, [scope.identity.admin, cache, installed])
  return (
    <FormPage
      title="GitHub App setup"
      description="Connect your user-owned GitHub App."
      breadcrumbs={[]}
    >
      <FormSection
        title={
          result
            ? 'Repositories connected'
            : setup
              ? 'App created'
              : error
                ? 'Setup needs attention'
                : 'Verifying GitHub'
        }
      >
        {!scope.identity.admin ? (
          <Note>Complete setup in the same administrator browser session that started it.</Note>
        ) : result ? (
          <>
            <p>
              {result.name} is connected to {result.account}. Its signed webhook is configured.
            </p>
            <Button asChild variant="primary">
              <Link
                to="/settings/git/connections/$connectionId"
                params={{ connectionId: result.id }}
              >
                Open connection
              </Link>
            </Button>
          </>
        ) : setup ? (
          <>
            <p>
              Your App credentials are saved securely. Choose repositories on GitHub to finish
              installation.
            </p>
            <Button
              variant="primary"
              onClick={() => {
                try {
                  openGitAppSetup(setup)
                } catch (cause) {
                  setError(message(cause))
                }
              }}
            >
              Choose repositories on GitHub
            </Button>
          </>
        ) : !error ? (
          <Loading />
        ) : null}
        {error && <ErrorState error={error} />}
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
