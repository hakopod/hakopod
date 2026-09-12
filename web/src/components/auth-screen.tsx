import { ServiceIcon } from './service-icon'
import { useEffect, useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { passkeyCredential } from '../lib/webauthn'
import { AuthCard } from '@hakopod/hatch-ui/blocks/auth-card'
import { Field } from '@hakopod/hatch-ui/components/field'
import { PasswordField } from '@hakopod/hatch-ui/components/password-field'
import { Icon } from './icons'
import { Button } from './ui/button'
import { ErrorState, Loading } from './shared'

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
  const setup = status.data?.setup_required && !inviteToken
  useEffect(() => {
    const controller = new AbortController()
    void fetch('/session', { signal: controller.signal, cache: 'no-store' })
      .then((response) => response.json())
      .then((body) => {
        if (body.mfa_required) {
          setProviderMFA(true)
          setMFA(true)
        }
      })
      .catch(() => {})
    return () => controller.abort()
  }, [])
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (busy) return
    if ((setup || (inviteToken && !signedIn)) && password !== confirmation) {
      setError('The passwords do not match.')
      return
    }
    if ((setup || (inviteToken && !signedIn)) && new TextEncoder().encode(password).length > 72) {
      setError('Use a password no longer than 72 UTF-8 bytes.')
      return
    }
    setBusy(true)
    setError('')
    try {
      await submitSession(
        providerMFA
          ? { action: 'mfa', code }
          : inviteToken
            ? {
                action: 'invite',
                invite_token: inviteToken,
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
      onSuccess()
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
      await unwrap(
        client.POST('/auth/passkeys/login/finish', {
          body: { challenge: start.challenge, credential },
        }),
      )
      onSuccess()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  const title = providerMFA
    ? 'Complete your sign-in'
    : inviteToken
      ? 'Join your team'
      : setup
        ? 'Make this workspace yours'
        : 'Welcome back'
  return (
    <div className={`hako-auth-page ${signedIn ? 'hako-auth-embedded' : ''}`}>
      {!signedIn && (
        <header className="hako-auth-header">
          <a href="/" className="hako-wordmark" aria-label="Hakopod home">
            <img
              className="hako-wordmark-dark"
              src="/brand/hakopod-horizontal-paper.svg"
              alt=""
              width="140"
            />
            <img
              className="hako-wordmark-light"
              src="/brand/hakopod-horizontal-ink.svg"
              alt=""
              width="140"
            />
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
      <div className="hako-auth-main bg-grid">
        <AuthCard
          className="hako-auth-card"
          title={title}
          footer={<span>Your infrastructure. Your team.</span>}
        >
          <p className="hako-auth-description">
            {providerMFA
              ? 'Enter an authenticator code or a recovery code.'
              : inviteToken
                ? signedIn
                  ? 'Accept this invitation using your current account.'
                  : 'Choose your name and password to accept this invitation. Existing members can sign in first.'
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
              {!setup &&
                !inviteToken &&
                !providerMFA &&
                (status.data?.passkeys || Boolean(status.data?.providers.length)) && (
                  <div className="hako-auth-alternatives">
                    {status.data?.passkeys && (
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
                    {status.data?.providers
                      .filter((provider) => ['github', 'google', 'gitlab'].includes(provider))
                      .map((provider) => (
                        <Button variant="outline" className="full-width" asChild key={provider}>
                          <a
                            href={`/api/v1/auth/oauth/${provider}/start`}
                            onClick={() => {
                              if (location.pathname.startsWith('/login/'))
                                sessionStorage.setItem(
                                  'hakopod-auth-return',
                                  location.pathname + location.search,
                                )
                            }}
                          >
                            <ServiceIcon name={provider} size={17} />
                            Continue with{' '}
                            {provider === 'github'
                              ? 'GitHub'
                              : provider === 'gitlab'
                                ? 'GitLab'
                                : 'Google'}
                          </a>
                        </Button>
                      ))}
                    <div className="hako-auth-divider">
                      <span>Or use your email</span>
                    </div>
                  </div>
                )}
              <form onSubmit={submit} className="hako-auth-form" aria-busy={busy}>
                {!providerMFA && (
                  <>
                    {(setup || (inviteToken && !signedIn)) && (
                      <Field
                        label="Your name"
                        value={name}
                        onChange={(event) => setName(event.target.value)}
                        autoComplete="name"
                        maxLength={100}
                        required
                      />
                    )}
                    {!inviteToken && (
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
                    {(!signedIn || !inviteToken) && (
                      <PasswordField
                        label="Password"
                        value={password}
                        onChange={(event) => setPassword(event.target.value)}
                        autoComplete={setup || inviteToken ? 'new-password' : 'current-password'}
                        minLength={setup || inviteToken ? 12 : undefined}
                        maxLength={72}
                        required
                      />
                    )}
                    {(setup || (inviteToken && !signedIn)) && (
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
                          Use the credential from this installation’s setup. An existing bootstrap
                          session can also claim ownership.
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
                {error && (
                  <div className="inline-error" role="alert">
                    {error}
                  </div>
                )}
                <Button variant="primary" className="full-width" type="submit" disabled={busy}>
                  {busy
                    ? 'Signing in…'
                    : providerMFA
                      ? 'Verify and continue'
                      : inviteToken
                        ? 'Accept invitation'
                        : setup
                          ? 'Create owner account'
                          : 'Sign in'}
                  <Icon name="arrow" size={16} />
                </Button>
              </form>
              {inviteToken && !signedIn && (
                <p className="field-help">
                  <a
                    href="/"
                    onClick={() =>
                      sessionStorage.setItem(
                        'hakopod-auth-return',
                        location.pathname + location.search,
                      )
                    }
                  >
                    Sign in with an existing account
                  </a>
                  , then accept your invitation.
                </p>
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
