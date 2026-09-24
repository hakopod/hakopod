import type { AuthView } from '../lib/auth-view'
import { Brand, brandLabel } from './brand'
import { dashboardEdition, EditionAuthAside } from '../lib/dashboard-edition'
import { ServiceIcon } from './service-icon'
import '../styles/account-access.css'
import { useEffect, useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { passkeyCredential } from '../lib/webauthn'
import { AuthCard } from '@hakopod/hatch-ui/blocks/auth-card'
import { Field } from './ui/field'
import { PasswordField } from './ui/password-field'
import { Icon } from './icons'
import { Button } from './ui/button'
import { ErrorState, Loading, RequestError } from './shared'

function authLocation() {
  if (typeof window === 'undefined') return { mode: 'login', token: '' }
  const fragment = window.location.hash
  return {
    mode: window.location.pathname.split('/')[2] || 'login',
    token:
      fragment.length <= 2048
        ? new URLSearchParams(fragment.slice(1)).get('token')?.slice(0, 512) || ''
        : '',
  }
}

export async function submitSession(body: Record<string, unknown>) {
  const response = await fetch('/session', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    cache: 'no-store',
  })
  const payload = await response.json()
  if (!response.ok)
    throw new APIError(
      payload.error?.message || 'Unable to sign in.',
      response.status,
      payload.error?.code,
    )
  return payload as {
    authenticated?: boolean
    onboarding_required?: boolean
    accepted?: boolean
    reset?: boolean
  }
}

export function AuthScreen({
  onSuccess,
  toggleTheme,
  theme,
  inviteToken = '',
  signedIn = false,
}: {
  onSuccess: () => void
  toggleTheme?: () => void
  theme?: string
  inviteToken?: string
  signedIn?: boolean
}) {
  const status = useQuery({
    queryKey: ['auth-status'],
    queryFn: ({ signal }) => unwrap(client.GET('/auth/status', { signal })),
    staleTime: 30000,
    retry: false,
  })
  const [location, setLocation] = useState(authLocation)
  const { mode, token: linkToken } = location
  const register = mode === 'signup' && !inviteToken
  const forgot = mode === 'forgot'
  const reset = mode === 'reset'
  const verify = mode === 'verify'
  const [sent, setSent] = useState(false)
  const [workspace, setWorkspace] = useState<'invite' | 'personal'>(() =>
    typeof window !== 'undefined' &&
    new URLSearchParams(window.location.search).get('workspace') === 'personal'
      ? 'personal'
      : 'invite',
  )
  const invite = useQuery({
    queryKey: ['invite-details', inviteToken],
    queryFn: ({ signal }) =>
      unwrap(client.POST('/auth/invites/inspect', { body: { token: inviteToken }, signal })),
    enabled: Boolean(inviteToken),
    retry: false,
    staleTime: 60000,
  })
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [installer, setInstaller] = useState('')
  const [code, setCode] = useState('')
  const [mfa, setMFA] = useState(false)
  const [providerMFA, setProviderMFA] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const setup = status.data?.setup_required && !inviteToken && !forgot && !reset && !verify
  const newPassword = setup || register || reset || Boolean(inviteToken && !signedIn)
  useEffect(() => {
    const syncLocation = () => {
      const next = authLocation()
      setLocation((current) =>
        current.mode === next.mode && current.token === next.token ? current : next,
      )
    }
    syncLocation()
    window.addEventListener('hashchange', syncLocation)
    window.addEventListener('popstate', syncLocation)
    return () => {
      window.removeEventListener('hashchange', syncLocation)
      window.removeEventListener('popstate', syncLocation)
    }
  }, [])
  useEffect(() => {
    setSent(false)
    setError('')
  }, [mode, linkToken])
  useEffect(() => {
    const controller = new AbortController()
    void fetch('/session', { signal: controller.signal, cache: 'no-store' })
      .then((response) => response.json())
      .then((body) => {
        if (body.mfa_required && mode === 'login' && !inviteToken) {
          setProviderMFA(true)
          setMFA(true)
        }
      })
      .catch(() => {})
    return () => controller.abort()
  }, [])
  useEffect(() => {
    rememberReturn()
  }, [])
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    if (newPassword && password !== confirmation) {
      setError('The passwords do not match.')
      return
    }
    if (newPassword && new TextEncoder().encode(password).length > 72) {
      setError('Use a password no longer than 72 UTF-8 bytes.')
      return
    }
    setBusy(true)
    setError('')
    try {
      const result = await submitSession(
        forgot
          ? { action: 'forgot', email }
          : reset
            ? { action: 'reset', token: linkToken, password }
            : verify
              ? { action: 'verify', token: linkToken }
              : register && !setup
                ? { action: 'register', name, email, password }
                : providerMFA
                  ? { action: 'mfa', code }
                  : inviteToken
                    ? {
                        action: 'invite',
                        invite_token: inviteToken,
                        workspace,
                        ...(signedIn ? {} : { name, password }),
                      }
                    : setup
                      ? { action: 'setup', name, email, password, installer_credential: installer }
                      : { action: 'login', email, password, code },
      )
      setPassword('')
      setConfirmation('')
      setInstaller('')
      setCode('')
      if (result.accepted || result.reset) setSent(true)
      else if (result.onboarding_required) window.location.assign('/login/onboarding')
      else onSuccess()
    } catch (err) {
      if (err instanceof APIError && err.code === 'mfa_required') setMFA(true)
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  async function passkey() {
    setBusy(true)
    setError('')
    try {
      const start = await unwrap(client.POST('/auth/passkeys/login/start', { body: {} }))
      const credential = await passkeyCredential(start.options.publicKey)
      const result = await unwrap(
        client.POST('/auth/passkeys/login/finish', {
          body: { challenge: start.challenge, credential },
        }),
      )
      if (result.onboarding_required) window.location.assign('/login/onboarding')
      else onSuccess()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  function rememberReturn() {
    const destination = new URL(window.location.href)
    if (!['/login/invite', '/login/device'].includes(destination.pathname)) return
    if (destination.pathname === '/login/invite')
      destination.searchParams.set('workspace', workspace)
    try {
      sessionStorage.setItem('hakopod-auth-return', destination.pathname + destination.search)
    } catch {}
  }
  const title = forgot
    ? 'Reset your password'
    : reset
      ? 'Choose a new password'
      : verify
        ? 'Verify your email'
        : register && !setup
          ? 'Create your account'
          : providerMFA
            ? 'Complete your sign-in'
            : inviteToken
              ? 'Choose your workspace'
              : setup
                ? 'Make this workspace yours'
                : 'Welcome back'
  const authView: AuthView = forgot
    ? 'forgot'
    : reset
      ? 'reset'
      : verify
        ? 'verify'
        : mfa || providerMFA
          ? 'mfa'
          : inviteToken
            ? 'invite'
            : setup
              ? 'setup'
              : register
                ? 'signup'
                : 'login'
  return (
    <div className={`hako-auth-page ${signedIn ? 'hako-auth-embedded' : ''}`}>
      {!signedIn && (
        <header className="hako-auth-header">
          <a
            href="/"
            className="hako-wordmark"
            data-edition-brand={Boolean(dashboardEdition.brandSuffix) || undefined}
            aria-label={`${brandLabel} home`}
          >
            <Brand icon={dashboardEdition.cloud} />
          </a>
          {toggleTheme && (
            <Button
              variant="ghost"
              size="icon"
              aria-label={`Use ${theme === 'dark' ? 'light' : 'dark'} theme`}
              onClick={toggleTheme}
            >
              <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
            </Button>
          )}
        </header>
      )}
      <div
        className="hako-auth-main bg-grid"
        data-auth-aside={(!signedIn && dashboardEdition.authAside) || undefined}
      >
        {!signedIn && <EditionAuthAside view={authView} />}
        <AuthCard
          className="hako-auth-card"
          title={title}
          footer={<span>Your infrastructure. Your team.</span>}
        >
          <p className="hako-auth-description">
            {forgot
              ? 'We will email a recovery link if this address has an account.'
              : reset
                ? 'Your two-factor authentication stays enabled. Your existing browser and CLI sessions will be signed out.'
                : verify
                  ? 'Confirm your email to finish registration, then choose your workspace.'
                  : register && !setup
                    ? 'Create a personal workspace or join a team that invited you.'
                    : providerMFA
                      ? 'Enter an authenticator code or a recovery code.'
                      : inviteToken
                        ? signedIn
                          ? 'Accept this invitation using your current account.'
                          : 'Choose how you want to get started. An invitation can create your account even when public registration is closed.'
                        : setup
                          ? 'Create the first owner account for this Hakopod installation.'
                          : 'Sign in to deploy and operate your applications.'}
          </p>
          {status.isPending ? (
            <Loading rows={2} />
          ) : status.error ? (
            <ErrorState error={status.error} retry={() => void status.refetch()} />
          ) : (
            <>
              {sent ? (
                <div role="status" className="hako-auth-description">
                  {reset
                    ? 'Your password has been changed. Sign in with your new password.'
                    : 'If this address is eligible, an email is on its way. The link expires in 15 minutes. Check your spam folder too.'}
                  <p>
                    <a href="/">Return to sign in</a>
                  </p>
                </div>
              ) : (
                <>
                  {register && !setup && !status.data?.signup_enabled ? (
                    <RequestError
                      error={'Registration is closed. Use an invitation from your administrator.'}
                    />
                  ) : null}
                  {(forgot || (register && !setup)) && !status.data?.email_delivery && (
                    <p className="field-help">
                      Email delivery is not configured.{' '}
                      {register
                        ? 'Use one of the configured providers below, or contact your administrator.'
                        : 'Contact your administrator for account recovery.'}
                    </p>
                  )}
                  {inviteToken && (
                    <fieldset className="hako-auth-workspace-choice">
                      <legend>Where would you like to start?</legend>
                      <label>
                        <input
                          type="radio"
                          name="workspace-choice"
                          value="invite"
                          checked={workspace === 'invite'}
                          onChange={() => setWorkspace('invite')}
                        />{' '}
                        Join the invited workspace
                      </label>
                      <label>
                        <input
                          type="radio"
                          name="workspace-choice"
                          value="personal"
                          checked={workspace === 'personal'}
                          onChange={() => setWorkspace('personal')}
                        />{' '}
                        Create a separate personal workspace
                      </label>
                      <p className="field-help">
                        A personal workspace is private to you. It does not grant access to the
                        inviting team.
                      </p>
                      {invite.data && (
                        <p className="field-help">
                          Invitation for {invite.data.email}
                          {invite.data.project ? ` · ${invite.data.project}` : ''}
                        </p>
                      )}
                      {invite.error && (
                        <ErrorState error={invite.error} retry={() => void invite.refetch()} />
                      )}
                    </fieldset>
                  )}
                  {!setup &&
                    !signedIn &&
                    !forgot &&
                    !reset &&
                    !verify &&
                    (!register || status.data?.signup_enabled) &&
                    !providerMFA &&
                    (status.data?.passkeys || Boolean(status.data?.providers.length)) && (
                      <div className="hako-auth-alternatives">
                        {status.data?.passkeys && !register && !inviteToken && (
                          <Button
                            variant="outline"
                            className="full-width"
                            disabled={busy}
                            onClick={() => void passkey()}
                          >
                            <Icon name="key" size={16} />
                            Sign in with a passkey
                          </Button>
                        )}
                        <div className="hako-auth-providers">
                          {['google', 'github', 'gitlab', 'oidc']
                            .filter((provider) => status.data?.providers.includes(provider))
                            .map((provider) => {
                              const label =
                                provider === 'github'
                                  ? 'GitHub'
                                  : provider === 'gitlab'
                                    ? 'GitLab'
                                    : provider === 'oidc'
                                      ? 'company SSO'
                                      : 'Google'
                              return (
                                <Button variant="outline" asChild key={provider}>
                                  <a
                                    data-provider={provider}
                                    href={`/api/v1/auth/oauth/${provider}/start${register || inviteToken ? `?${new URLSearchParams({ intent: 'register', ...(inviteToken ? { invite_token: inviteToken } : {}) })}` : ''}`}
                                    onClick={rememberReturn}
                                    aria-label={`Continue with ${label}`}
                                  >
                                    {provider === 'oidc' ? (
                                      <Icon name="shield" size={20} />
                                    ) : provider === 'google' || provider === 'gitlab' ? (
                                      <img
                                        className="auth-provider-logo"
                                        src={`/icons/${provider}-color.svg`}
                                        alt=""
                                        width={20}
                                        height={20}
                                      />
                                    ) : (
                                      <ServiceIcon name={provider} size={20} />
                                    )}
                                    {provider === 'google' || provider === 'oidc'
                                      ? `Continue with ${label}`
                                      : label}
                                  </a>
                                </Button>
                              )
                            })}
                        </div>
                        <div className="hako-auth-divider">
                          <span>Or use your email</span>
                        </div>
                      </div>
                    )}
                  <form onSubmit={submit} className="hako-auth-form" aria-busy={busy}>
                    {!providerMFA && !verify && (
                      <>
                        {(setup || register || (inviteToken && !signedIn)) && !reset && !forgot && (
                          <Field
                            label="Your name"
                            value={name}
                            onChange={(event) => setName(event.target.value)}
                            autoComplete="name"
                            maxLength={100}
                            required
                          />
                        )}
                        {!inviteToken && !reset && (
                          <Field
                            label="Email address"
                            type="email"
                            value={email}
                            onChange={(event) => setEmail(event.target.value)}
                            autoComplete="username"
                            maxLength={254}
                            required
                          />
                        )}
                        {(!signedIn || !inviteToken) && !forgot && (
                          <PasswordField
                            label="Password"
                            value={password}
                            onChange={(event) => setPassword(event.target.value)}
                            autoComplete={newPassword ? 'new-password' : 'current-password'}
                            minLength={newPassword ? 12 : undefined}
                            maxLength={72}
                            required
                          />
                        )}
                        {newPassword && !forgot && (
                          <>
                            <PasswordField
                              label="Confirm password"
                              value={confirmation}
                              onChange={(event) => setConfirmation(event.target.value)}
                              autoComplete="new-password"
                              minLength={12}
                              maxLength={72}
                              required
                            />
                            <p className="field-help">Use at least 12 characters.</p>
                          </>
                        )}
                        {setup && (
                          <>
                            <PasswordField
                              label="Installer credential"
                              value={installer}
                              onChange={(event) => setInstaller(event.target.value)}
                              autoComplete="off"
                              maxLength={512}
                              placeholder="Setup token or bootstrap administrator key"
                            />
                            <p className="field-help">
                              Use the credential from this installation’s setup. An existing
                              bootstrap session can also claim ownership.
                            </p>
                          </>
                        )}
                      </>
                    )}
                    {mfa && (
                      <Field
                        label="Authenticator or recovery code"
                        value={code}
                        onChange={(event) => setCode(event.target.value)}
                        autoComplete="one-time-code"
                        maxLength={128}
                        required
                        autoFocus
                      />
                    )}
                    {error && <RequestError error={error} />}
                    <Button
                      variant="primary"
                      className="full-width"
                      type="submit"
                      disabled={
                        busy ||
                        ((verify || reset) && !linkToken) ||
                        (register &&
                          !setup &&
                          (!status.data?.signup_enabled || !status.data?.email_delivery)) ||
                        (forgot && !status.data?.password_recovery) ||
                        Boolean(inviteToken && !invite.data)
                      }
                    >
                      {busy
                        ? 'Please wait…'
                        : forgot
                          ? 'Send recovery link'
                          : reset
                            ? 'Save new password'
                            : verify
                              ? 'Verify and continue'
                              : register && !setup
                                ? 'Create account'
                                : providerMFA
                                  ? 'Verify and continue'
                                  : inviteToken
                                    ? workspace === 'personal'
                                      ? 'Create personal workspace'
                                      : 'Accept invitation'
                                    : setup
                                      ? 'Create owner account'
                                      : 'Sign in'}
                      <Icon name="arrow" size={16} />
                    </Button>
                  </form>
                  {(verify || reset) && !linkToken && (
                    <RequestError
                      error={'This link is missing its token. Open the full link from your email.'}
                    />
                  )}
                  {!setup && !inviteToken && !providerMFA && (
                    <div className="hako-auth-links">
                      {register || forgot || reset || verify ? (
                        <a href="/">Back to sign in</a>
                      ) : (
                        <>
                          <a href="/login/forgot">Forgot password?</a>
                          {status.data?.signup_enabled && (
                            <a href="/login/signup">Create an account</a>
                          )}
                        </>
                      )}
                    </div>
                  )}
                  {inviteToken && !signedIn && (
                    <p className="field-help">
                      <a href="/" onClick={rememberReturn}>
                        Sign in with an existing account
                      </a>
                      , then accept your invitation.
                    </p>
                  )}
                </>
              )}
            </>
          )}
          <p className="hako-auth-assurance">
            <Icon name="lock" size={14} />
            <span>Your browser session uses a secure, HttpOnly cookie.</span>
          </p>
        </AuthCard>
      </div>
    </div>
  )
}
