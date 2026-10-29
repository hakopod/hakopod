import { useEditionFeatures } from '../lib/dashboard-edition'
import { useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { queryOptions, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import { useLicense } from '../lib/license'
import {
  loginProviders,
  loginProviderNames,
  loginProviderFeature,
  prepareLoginProvider,
  type LoginProvider,
  type LoginProviderSettings,
  type SecretAction,
} from '../lib/installation-settings'
import { InstallationAccess } from './installation-access'
import { FormPage, FormSection } from './form-page'
import {
  InstallationCredential,
  InstallationReviewRows,
  credentialSummary,
  useInstallationFormFocus,
} from './installation-form-fields'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Copy, ErrorState, Loading, Note, RequestError } from './shared'
import { SettingsRow, SettingsRows, SettingsSection, SettingsState } from './settings-section'
import { ServiceIcon } from './service-icon'
import { Icon } from './icons'

function providerQuery(provider: LoginProvider) {
  return queryOptions({
    queryKey: ['installation-login-provider', provider],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/installation/login-providers/{provider}', {
          signal,
          params: { path: { provider } },
        }),
      ),
    staleTime: 30000,
    refetchOnWindowFocus: false,
    retry: false,
  })
}
function useProvider(provider: LoginProvider) {
  return useQuery(providerQuery(provider))
}
function ProviderMark({ provider }: { provider: LoginProvider }) {
  return provider === 'oidc' ? (
    <Icon name="shield" size={16} />
  ) : (
    <ServiceIcon name={provider} size={16} loading="eager" />
  )
}
export function LoginProviderSettingsPanel() {
  const queries = useQueries({ queries: loginProviders.map(providerQuery) })
  const license = useLicense()
  const features = useEditionFeatures()
  const entitled = (provider: LoginProvider) =>
    features.operator ||
    Boolean(
      license.data?.catalog.find((item) => item.id === loginProviderFeature(provider))?.enabled,
    )
  const loaded = queries.every((query) => query.data)
  const enabled = loginProviders.filter(
    (provider, index) => queries[index].data?.enabled && entitled(provider),
  )
  return (
    <InstallationAccess>
      <SettingsSection
        title="Sign-in providers"
        help="Connect an identity provider for existing users. Self-hosted installations keep public signup closed."
        state={
          loaded && !license.isPending
            ? enabled.length
              ? `${enabled.map((provider) => loginProviderNames[provider]).join(', ')} enabled · public signup stays closed`
              : 'Email and password only · public signup stays closed'
            : undefined
        }
        actions={
          loaded &&
          !license.isPending &&
          loginProviders.some((provider) => !entitled(provider)) && (
            <Button asChild>
              <Link to="/settings" search={{ tab: 'license' }}>
                View license
              </Link>
            </Button>
          )
        }
      >
        {license.error && (
          <ErrorState error={license.error} retry={() => void license.refetch()} />
        )}
        <SettingsRows label="Sign-in providers">
          {loginProviders.map((provider, index) => {
            const query = queries[index]
            const allowed = entitled(provider)
            const plan = provider === 'oidc' ? 'Enterprise' : 'Pro'
            return (
              <SettingsRow
                key={provider}
                mark={<ProviderMark provider={provider} />}
                name={loginProviderNames[provider]}
                detail={`${plan} plan`}
                status={
                  query.isPending || license.isPending ? (
                    <SettingsState tone="muted">Checking…</SettingsState>
                  ) : query.error ? (
                    <SettingsState tone="danger">Unavailable</SettingsState>
                  ) : license.error ? (
                    <SettingsState tone="muted">License unknown</SettingsState>
                  ) : !allowed ? (
                    <SettingsState tone="warn">License required</SettingsState>
                  ) : query.data?.enabled ? (
                    <SettingsState tone="ok">Enabled</SettingsState>
                  ) : (
                    <SettingsState tone="muted">Disabled</SettingsState>
                  )
                }
                meta={
                  query.error
                    ? message(query.error)
                    : query.data?.enabled && !allowed && !license.isPending && !license.error
                      ? 'Saved but off for sign-in until the entitlement is restored.'
                      : query.data
                        ? query.data.secret_configured
                          ? 'Client secret stored'
                          : 'No client secret stored'
                        : undefined
                }
                actions={
                  query.error ? (
                    <Button size="sm" onClick={() => void query.refetch()}>
                      Retry
                    </Button>
                  ) : (
                    <>
                      <Button size="sm" asChild>
                        <Link
                          to="/settings/login-providers/$provider"
                          params={{ provider }}
                          aria-label={`${allowed ? 'Configure' : 'View'} ${loginProviderNames[provider]} sign-in`}
                        >
                          {allowed ? 'Configure' : 'View'}
                        </Link>
                      </Button>
                    </>
                  )
                }
              />
            )
          })}
        </SettingsRows>
      </SettingsSection>
    </InstallationAccess>
  )
}
export function LoginProviderEditor({ provider }: { provider: LoginProvider }) {
  return (
    <InstallationAccess>
      <ProviderEditorLoader provider={provider} />
    </InstallationAccess>
  )
}
function ProviderEditorLoader({ provider }: { provider: LoginProvider }) {
  const features = useEditionFeatures()
  const query = useProvider(provider)
  const license = useLicense()
  if (query.isPending || license.isPending) return <Loading />
  if (query.error || license.error || !query.data)
    return (
      <ErrorState
        error={query.error || license.error}
        retry={() => {
          void query.refetch()
          void license.refetch()
        }}
      />
    )
  const entitled =
    features.operator ||
    Boolean(
      license.data?.catalog.find((item) => item.id === loginProviderFeature(provider))?.enabled,
    )
  return <ProviderForm key={provider} current={query.data} entitled={entitled} />
}
function ProviderForm({
  current,
  entitled,
}: {
  current: LoginProviderSettings
  entitled: boolean
}) {
  const [draft, setDraft] = useState(current)
  const [action, setAction] = useState<SecretAction>('keep')
  const [secret, setSecret] = useState('')
  const [review, setReview] = useState(false)
  const formRef = useInstallationFormFocus(review)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)
  const cache = useQueryClient(),
    navigate = useNavigate()
  const provider = draft.provider
  const canSave = current.encryption_ready && (entitled || (current.enabled && !draft.enabled))
  const change = <K extends keyof LoginProviderSettings>(key: K, value: LoginProviderSettings[K]) =>
    setDraft((old) => ({ ...old, [key]: value }))
  async function save() {
    if (busy || conflict || !canSave) return
    setBusy(true)
    setError('')
    try {
      const result = await unwrap(
        client.PUT('/installation/login-providers/{provider}', {
          params: { path: { provider } },
          body: prepareLoginProvider(draft, action, secret),
        }),
      )
      setSecret('')
      cache.setQueryData(['installation-login-provider', provider], result)
      await cache.invalidateQueries({ queryKey: ['auth-status'] })
      await navigate({ to: '/settings', search: { tab: 'login-providers' } })
    } catch (cause) {
      setError(message(cause))
      if (cause instanceof APIError && cause.status === 409) {
        setConflict(true)
        void cache.invalidateQueries({ queryKey: ['installation-login-provider', provider] })
      }
    } finally {
      setBusy(false)
    }
  }
  return (
    <FormPage
      title={`${loginProviderNames[provider]} sign-in`}
      description="Review the callback, credentials and availability before saving."
      breadcrumbs={[]}
    >
      <form
        ref={formRef}
        tabIndex={-1}
        aria-label={review ? 'Sign-in provider review' : 'Sign-in provider configuration'}
        className="grid gap-4"
        onSubmit={(event) => {
          event.preventDefault()
          if (busy) return
          if (review) {
            void save()
            return
          }
          try {
            prepareLoginProvider(draft, action, secret)
            setError('')
            setReview(true)
          } catch (cause) {
            setError(message(cause))
          }
        }}
      >
        {!entitled && (
          <Note>
            {provider === 'oidc' ? 'Enterprise SSO' : 'OAuth login'} requires an active entitlement.
            You can inspect saved settings and turn off an enabled provider.
          </Note>
        )}
        {!current.encryption_ready && (
          <Note>Configure the installation encryption key before saving provider credentials.</Note>
        )}
        {review ? (
          <FormSection title="Review sign-in settings">
            <InstallationReviewRows
              rows={[
                ['Provider', loginProviderNames[provider]],
                ['Sign-in', draft.enabled ? 'Enabled' : 'Disabled'],
                ['Client ID', draft.client_id || 'None'],
                ...(provider === 'oidc'
                  ? [['Issuer URL', draft.issuer_url || 'None'] as [string, string]]
                  : []),
                ['Client secret', credentialSummary(action, current.secret_configured)],
                ['Callback URL', draft.callback_url],
                ['Revision', String(draft.revision)],
              ]}
            />
            <p className="field-help">Self-hosted sign-in does not open public signup.</p>
          </FormSection>
        ) : (
          <>
            <FormSection title="Provider">
              <label className="checkbox-label">
                <Input
                  type="checkbox"
                  checked={draft.enabled}
                  disabled={!entitled && !draft.enabled}
                  onChange={(event) => change('enabled', event.target.checked)}
                />
                Enable sign-in
              </label>
              <label>
                Client ID
                <Input
                  value={draft.client_id}
                  disabled={!entitled}
                  required={draft.enabled}
                  maxLength={512}
                  onChange={(event) => change('client_id', event.target.value)}
                  autoComplete="off"
                />
              </label>
              {provider === 'oidc' && (
                <label>
                  Issuer URL
                  <Input
                    type="url"
                    required={draft.enabled}
                    disabled={!entitled}
                    value={draft.issuer_url}
                    maxLength={2048}
                    placeholder="https://identity.example.com"
                    onChange={(event) => change('issuer_url', event.target.value)}
                  />
                  <small className="field-help">
                    Use the issuer URL published by your OpenID Connect provider.
                  </small>
                </label>
              )}
              <div>
                <span className="text-sm">Callback URL</span>
                <div className="flex items-start gap-2">
                  <code className="min-w-0 break-all">{draft.callback_url}</code>
                  <Copy value={draft.callback_url} label="Copy callback URL" />
                </div>
                <p className="field-help">
                  Register this exact callback with your identity provider.
                </p>
              </div>
            </FormSection>
            {entitled && (
              <FormSection title="Credentials">
                <InstallationCredential
                  label="Client secret"
                  configured={current.secret_configured}
                  action={action}
                  onAction={setAction}
                  value={secret}
                  onValue={setSecret}
                />
              </FormSection>
            )}
          </>
        )}
        {error && <RequestError error={error} />}
        {conflict && (
          <Note>
            Another administrator changed these settings. Your draft is kept. Return to settings and
            reopen the provider to review its latest revision.
          </Note>
        )}
        <div className="form-footer">
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() =>
              review
                ? setReview(false)
                : void navigate({ to: '/settings', search: { tab: 'login-providers' } })
            }
          >
            {review ? 'Back to edit' : 'Cancel'}
          </Button>
          <Button type="submit" variant="primary" disabled={busy || conflict || !canSave}>
            {busy ? 'Saving…' : review ? 'Save provider' : 'Review changes'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
