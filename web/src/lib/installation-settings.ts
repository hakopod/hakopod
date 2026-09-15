import { useEditionFeatures } from './dashboard-edition'
import { useQuery } from '@tanstack/react-query'
import type { components } from './api.generated'
import { client, unwrap } from './client'
import { useScope } from './scope'

export const loginProviderNames = {
  github: 'GitHub',
  google: 'Google',
  gitlab: 'GitLab',
  oidc: 'Enterprise SSO',
} as const
export type LoginProvider = keyof typeof loginProviderNames
export const loginProviders = Object.keys(loginProviderNames) as LoginProvider[]
export type LoginProviderSettings = components['schemas']['InstallationLoginProvider']
export type SMTPSettings = components['schemas']['InstallationSMTP']
export type SecretAction = 'keep' | 'replace' | 'clear'

export function useAuthStatus() {
  return useQuery({
    queryKey: ['auth-status'],
    queryFn: ({ signal }) => unwrap(client.GET('/auth/status', { signal })),
    staleTime: 30000,
    retry: false,
  })
}

export function useInstallationAccess() {
  const scope = useScope()
  const status = useAuthStatus()
  const features = useEditionFeatures()
  return {
    status,
    admin: scope.identity.admin,
    allowed:
      scope.identity.admin && (status.data?.deployment_mode === 'self-hosted' || features.operator),
  }
}

export function loginProviderFeature(provider: LoginProvider) {
  return provider === 'oidc' ? 'enterprise_sso' : 'oauth_login'
}

export function prepareLoginProvider(
  settings: LoginProviderSettings,
  action: SecretAction,
  secret: string,
): components['schemas']['InstallationLoginProviderInput'] {
  if (action === 'replace' && !secret) throw new Error('Enter the new client secret.')
  if (
    settings.enabled &&
    (!settings.client_id.trim() ||
      action === 'clear' ||
      (action === 'keep' && !settings.secret_configured))
  )
    throw new Error('An enabled provider needs a client ID and client secret.')
  if (settings.provider === 'oidc' && (settings.enabled || settings.issuer_url)) {
    let issuer: URL
    try {
      issuer = new URL(settings.issuer_url)
    } catch {
      throw new Error('Enter the HTTPS issuer URL from your identity provider.')
    }
    if (
      issuer.protocol !== 'https:' ||
      issuer.username ||
      issuer.password ||
      issuer.search ||
      issuer.hash
    )
      throw new Error('Use an HTTPS issuer URL without credentials, query or fragment.')
  }
  return {
    enabled: settings.enabled,
    client_id: settings.client_id.trim(),
    issuer_url: settings.provider === 'oidc' ? settings.issuer_url.trim() : '',
    expected_revision: settings.revision,
    ...(action === 'replace' ? { client_secret: secret } : {}),
    ...(action === 'clear' ? { clear_secret: true } : {}),
  }
}

export function prepareSMTP(
  settings: SMTPSettings,
  action: SecretAction,
  password: string,
): components['schemas']['InstallationSMTPInput'] {
  if (action === 'replace' && !password) throw new Error('Enter the new SMTP password.')
  const bytes = (value: string) => new TextEncoder().encode(value).length
  if (bytes(settings.host.trim()) > 253) throw new Error('Use an SMTP host of at most 253 bytes.')
  if (
    bytes(settings.username.trim()) > 256 ||
    /[\u0000-\u001f\u007f-\u009f]/.test(settings.username)
  )
    throw new Error('Use an SMTP username of at most 256 bytes without control characters.')
  if (bytes(settings.from_email.trim()) > 254)
    throw new Error('Use a sender email of at most 254 bytes.')
  if (action === 'replace' && (bytes(password) > 4096 || /[\0\r\n]/.test(password)))
    throw new Error('Use an SMTP password of at most 4096 bytes without NUL or line breaks.')

  if (!Number.isInteger(settings.port) || settings.port < 1 || settings.port > 65535)
    throw new Error('Use a port from 1 to 65535.')
  if (settings.enabled && (!settings.host.trim() || !settings.from_email.trim()))
    throw new Error('Enabled email delivery needs an SMTP host and sender address.')
  if (
    settings.enabled &&
    settings.username.trim() &&
    (action === 'clear' || (action === 'keep' && !settings.password_set))
  )
    throw new Error('Enter a password for SMTP authentication, or leave the username empty.')
  return {
    expected_revision: settings.revision,
    enabled: settings.enabled,
    host: settings.host.trim(),
    port: settings.port,
    security: settings.security,
    username: settings.username.trim(),
    from_email: settings.from_email.trim(),
    ...(action === 'replace' ? { password } : {}),
    ...(action === 'clear' ? { clear_password: true } : {}),
  }
}
