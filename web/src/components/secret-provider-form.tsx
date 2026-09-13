import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import type { Project } from '../lib/types'
import {
  prepareProviderInput,
  providerConfiguration,
  sameProviderSource,
  type ProviderCredentials,
  type ProviderInput,
  type SecretProvider,
} from '../lib/secret-providers'
import { FormPage, FormSection } from './form-page'
import { SecretProviderScopes } from './secret-provider-scopes'
import { SecretProviderReview } from './secret-provider-review'
import { SecretProviderSourceFields } from './secret-provider-source-fields'
import {
  SecretProviderCredentialFields,
  SecretProviderNetworkFields,
} from './secret-provider-credential-fields'
import { Button } from './ui/button'
import { Note } from './shared'

export function SecretProviderForm({
  provider,
  projects,
}: {
  provider?: SecretProvider
  projects: Project[]
}) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [configuration, setConfiguration] = useState(() => providerConfiguration(provider))
  const [privateNetworks, setPrivateNetworks] = useState((provider?.private_cidrs || []).join('\n'))
  const [credentials, setCredentials] = useState<ProviderCredentials>({
    token: '',
    client_id: '',
    client_secret: '',
  })
  const [replaceCredentials, setReplaceCredentials] = useState(!provider)
  const [review, setReview] = useState<Omit<ProviderInput, 'credentials'> | null>(null)
  const [revision, setRevision] = useState(provider?.revision || 0)
  const [current, setCurrent] = useState<SecretProvider>()
  const [conflict, setConflict] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)
  const heading = useRef<HTMLDivElement | null>(null)
  const hasReviewed = useRef(false)
  useEffect(() => () => controller.current?.abort(), [])
  useEffect(() => {
    if (review) hasReviewed.current = true
    if (hasReviewed.current) {
      heading.current?.focus()
      heading.current?.scrollIntoView({ block: 'start' })
    }
  }, [Boolean(review)])
  function input() {
    return prepareProviderInput(
      configuration,
      privateNetworks,
      credentials,
      replaceCredentials,
      revision,
    )
  }
  function showReview() {
    setError('')
    try {
      const { credentials: _credentials, ...publicConfiguration } = input()
      setReview(publicConfiguration)
    } catch (cause) {
      setError(message(cause))
    }
  }
  async function save() {
    if (!review || busy || conflict) return
    setBusy(true)
    setError('')
    const request = new AbortController()
    controller.current = request
    try {
      const result = await unwrap(
        client.PUT('/secret-providers/{name}', {
          signal: request.signal,
          params: { path: { name: configuration.name.trim() } },
          body: input(),
        }),
      )
      setCredentials({ token: '', client_id: '', client_secret: '' })
      void cache.invalidateQueries({ queryKey: ['secret-providers'] })
      void cache.invalidateQueries({ queryKey: ['secret-provider', result.name] })
      await navigate({ to: '/settings/secret-providers', search: { saved: result.name } })
    } catch (cause) {
      if (!request.signal.aborted) {
        setError(message(cause))
        if (cause instanceof APIError && cause.status === 409) {
          if (provider) setConflict(true)
          else {
            setReview(null)
            setError(
              'A provider with this name already exists. Your entries are kept; choose another name.',
            )
          }
        }
      }
    } finally {
      if (!request.signal.aborted) setBusy(false)
    }
  }
  async function loadCurrent() {
    if (!provider || busy) return
    setBusy(true)
    setError('')
    const request = new AbortController()
    controller.current = request
    try {
      const latest = await unwrap(
        client.GET('/secret-providers/{name}', {
          signal: request.signal,
          params: { path: { name: provider.name } },
        }),
      )
      if (!sameProviderSource(input(), latest))
        throw new Error(
          'This name now has a different source. Your entries are kept; return to the provider list and reopen its current configuration.',
        )
      setCurrent(latest)
    } catch (cause) {
      if (!request.signal.aborted) setError(message(cause))
    } finally {
      if (!request.signal.aborted) setBusy(false)
    }
  }
  const readonly = Boolean(provider)
  return (
    <FormPage
      title={provider ? `Edit ${provider.name}` : 'Add secret provider'}
      description="Connect external secrets and choose the projects that can use them."
      breadcrumbs={[
        { label: 'Settings', to: '/settings' },
        { label: 'Secret providers', to: '/settings/secret-providers' },
        { label: provider ? provider.name : 'Add provider' },
      ]}
    >
      <div
        className="muted-text mb-4 text-sm"
        ref={heading}
        tabIndex={-1}
        aria-label={review ? 'Review secret provider' : 'Configure secret provider'}
      >
        {review ? '2 / 2 · Review' : '1 / 2 · Configure'}
      </div>
      <form
        autoComplete="off"
        onSubmit={(event) => {
          event.preventDefault()
          if (review) void save()
          else showReview()
        }}
      >
        {review ? (
          <SecretProviderReview
            configuration={review}
            replacingCredentials={replaceCredentials}
            current={current}
          />
        ) : (
          <div className="grid gap-4">
            <SecretProviderSourceFields
              configuration={configuration}
              setConfiguration={setConfiguration}
              readonly={readonly}
            />
            <SecretProviderCredentialFields
              configuration={configuration}
              readonly={readonly}
              credentials={credentials}
              setCredentials={setCredentials}
              replaceCredentials={replaceCredentials}
              setReplaceCredentials={setReplaceCredentials}
            />
            <FormSection title="Access">
              <SecretProviderScopes
                projects={projects}
                scopes={configuration.scopes}
                onChange={(scopes) => setConfiguration({ ...configuration, scopes })}
              />
            </FormSection>
            <SecretProviderNetworkFields
              configuration={configuration}
              setConfiguration={setConfiguration}
              readonly={readonly}
              privateNetworks={privateNetworks}
              setPrivateNetworks={setPrivateNetworks}
            />
          </div>
        )}
        {error && (
          <div className="inline-error mt-4" role="alert">
            {error}
          </div>
        )}
        {conflict && (
          <div className="mt-4 grid gap-3">
            <Note>
              The provider changed after you opened it. Your entries are kept. Compare the latest
              access before choosing to apply your draft.
            </Note>
            <div className="flex flex-wrap gap-2">
              <Button type="button" disabled={busy} onClick={() => void loadCurrent()}>
                {busy ? 'Loading…' : 'Compare latest access'}
              </Button>
              {current && (
                <Button
                  type="button"
                  disabled={busy}
                  onClick={() => {
                    setRevision(current.revision)
                    setReview((value) =>
                      value ? { ...value, expected_revision: current.revision } : null,
                    )
                    setConflict(false)
                    setError('')
                  }}
                >
                  Use revision {current.revision} and keep my draft
                </Button>
              )}
            </div>
          </div>
        )}
        <div className="form-footer">
          {review ? (
            <Button type="button" disabled={busy} onClick={() => setReview(null)}>
              Back to configuration
            </Button>
          ) : (
            <Button asChild>
              <Link to="/settings/secret-providers" search={{}}>
                Cancel
              </Link>
            </Button>
          )}
          <Button
            type="submit"
            variant="primary"
            disabled={busy || conflict || (!review && projects.length === 0)}
          >
            {busy
              ? 'Saving…'
              : review
                ? provider
                  ? 'Save provider'
                  : 'Create provider'
                : 'Review provider'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
