import { useEditionFeatures } from '../lib/dashboard-edition'
import { useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
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
import { Card, Badge } from './ui/surfaces'
import { Copy, ErrorState, HeadingHelp, Loading, Note } from './shared'
import { ServiceIcon } from './service-icon'
import { Icon } from './icons'

function useProvider(provider: LoginProvider) {
  return useQuery({
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
function ProviderMark({ provider }: { provider: LoginProvider }) {
  return provider === 'oidc' ? (
    <Icon name="shield" size={24} />
  ) : (
    <ServiceIcon name={provider} size={24} />
  )
}
export function LoginProviderSettingsPanel() {
  return (
    <InstallationAccess>
      <div className="grid gap-4">
        <div className="hako-section-heading-title">
          <h2>Sign-in providers</h2>
          <HeadingHelp title="Sign-in providers">
            Connect an identity provider for existing users. Self-hosted installations keep public
            signup closed.
          </HeadingHelp>
        </div>
        <div className="grid gap-4 md:grid-cols-2">
          {loginProviders.map((provider) => (
            <ProviderCard key={provider} provider={provider} />
          ))}
        </div>
      </div>
    </InstallationAccess>
  )
}
function ProviderCard({ provider }: { provider: LoginProvider }) {
  const query = useProvider(provider)
  const license = useLicense()
  const features = useEditionFeatures()
  const entitled =
    features.operator ||
    Boolean(
      license.data?.catalog.find((item) => item.id === loginProviderFeature(provider))?.enabled,
    )
  return (
    <Card className="grid content-start gap-3 p-4">
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <ProviderMark provider={provider} />
          <h3 className="m-0">{loginProviderNames[provider]}</h3>
        </div>
        <Badge tone={entitled ? 'neutral' : 'accent'}>
          {provider === 'oidc' ? 'Enterprise' : 'Pro'}
        </Badge>
      </div>
      {query.isPending ? (
        <Loading rows={1} />
      ) : query.error ? (
        <ErrorState error={query.error} retry={() => void query.refetch()} />
      ) : (
        <>
          <p className="m-0 text-sm">
            {license.isPending
              ? 'Checking license…'
              : license.error
                ? 'License status unavailable'
                : !entitled
                  ? 'License required for sign-in'
                  : query.data?.enabled
                    ? 'Sign-in enabled'
                    : 'Sign-in disabled'}
          </p>
          {query.data?.enabled && !entitled && !license.isPending && !license.error && (
            <p className="field-help">
              The saved provider stays disabled for sign-in until its entitlement is restored. You
              can turn it off below.
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" asChild>
              <Link to="/settings/login-providers/$provider" params={{ provider }}>
                {entitled ? 'Configure' : 'View settings'}
              </Link>
            </Button>
            {!entitled && (
              <Button size="sm" variant="outline" asChild>
                <Link to="/settings" search={{ tab: 'license' }}>
                  View license
                </Link>
              </Button>
            )}
          </div>
        </>
      )}
      {license.error && <ErrorState error={license.error} retry={() => void license.refetch()} />}
    </Card>
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
        {error && (
          <div className="inline-error" role="alert">
            {error}
          </div>
        )}
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
