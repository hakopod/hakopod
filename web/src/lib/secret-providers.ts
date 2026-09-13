import type { components } from './api.generated'

export type SecretProvider = components['schemas']['SecretProvider']
export type ProviderInput = components['schemas']['SecretProviderInput']
export type ProviderScope = components['schemas']['SecretProviderScope']
export type ProviderConfiguration = Omit<ProviderInput, 'credentials' | 'expected_revision'> & {
  name: string
}
export type ProviderCredentials = { token: string; client_id: string; client_secret: string }
export const providerNames = { vault: 'HashiCorp Vault / OpenBao', infisical: 'Infisical' } as const

export function providerConfiguration(provider?: SecretProvider): ProviderConfiguration {
  return {
    name: provider?.name || '',
    kind: provider?.kind || 'vault',
    endpoint: provider?.endpoint || '',
    mount: provider?.mount || (provider?.kind === 'infisical' ? '' : 'secret'),
    root_path: provider?.root_path || '',
    project_id: provider?.project_id || '',
    environment: provider?.environment || '',
    namespace: provider?.namespace || '',
    ca_cert: provider?.ca_cert || '',
    private_cidrs: [...(provider?.private_cidrs || [])],
    scopes: (provider?.scopes || []).map((scope) => ({
      ...scope,
      environments: [...(scope.environments || [])],
    })),
  }
}

const namePattern = /^[a-z](?:[a-z0-9-]{0,38}[a-z0-9])?$/
function validPath(path: string) {
  return (
    path === '' ||
    (path.length <= 512 &&
      path
        .split('/')
        .every((part) => /^[A-Za-z0-9_.-]+$/.test(part) && part !== '.' && part !== '..'))
  )
}

export function prepareProviderInput(
  configuration: ProviderConfiguration,
  privateNetworks: string,
  credentials: ProviderCredentials,
  replaceCredentials: boolean,
  expectedRevision: number,
): ProviderInput {
  const next = {
    ...configuration,
    name: configuration.name.trim(),
    endpoint: configuration.endpoint.trim().replace(/\/$/, ''),
    private_cidrs: privateNetworks.split(/[\s,]+/).filter(Boolean),
  }
  if (!namePattern.test(next.name))
    throw new Error(
      'Use 1–40 lowercase letters, digits or hyphens for the name, starting with a letter and ending with a letter or digit.',
    )
  let url: URL
  try {
    url = new URL(next.endpoint)
  } catch {
    throw new Error('Enter the HTTPS origin of your provider.')
  }
  if (
    url.protocol !== 'https:' ||
    url.username ||
    url.password ||
    url.search ||
    url.hash ||
    url.pathname !== '/'
  )
    throw new Error('Use an HTTPS origin without credentials, a path or a query.')
  if (
    !validPath(next.root_path || '') ||
    (next.kind === 'vault' && !validPath(next.namespace || ''))
  )
    throw new Error('Use relative root and namespace paths without traversal or URL escapes.')
  if (next.kind === 'vault' && (!next.mount || !validPath(next.mount)))
    throw new Error('Enter the Vault KV v2 mount.')
  if (
    next.kind === 'infisical' &&
    (!next.project_id ||
      !/^[A-Za-z0-9_.-]+$/.test(next.project_id) ||
      !namePattern.test(next.environment || ''))
  )
    throw new Error('Enter the Infisical project ID and environment slug.')
  if (next.scopes.length < 1 || next.scopes.length > 32)
    throw new Error('Select between 1 and 32 projects that may use this provider.')
  if (next.private_cidrs.length > 16) throw new Error('Use at most 16 private network CIDRs.')
  const input: ProviderInput = { ...next, expected_revision: expectedRevision }
  if (next.kind === 'vault') {
    delete input.project_id
    delete input.environment
  } else {
    delete input.mount
    delete input.namespace
  }
  if (replaceCredentials) {
    const values =
      next.kind === 'vault'
        ? [credentials.token]
        : [credentials.client_id, credentials.client_secret]
    if (values.some((value) => !value || value.length > 8192 || /[\r\n\0]/.test(value)))
      throw new Error(
        next.kind === 'vault'
          ? 'Enter a Vault token without line breaks.'
          : 'Enter both the Infisical client ID and client secret without line breaks.',
      )
    input.credentials =
      next.kind === 'vault'
        ? { token: credentials.token }
        : { client_id: credentials.client_id, client_secret: credentials.client_secret }
  } else if (expectedRevision === 0) throw new Error('Credentials are required for a new provider.')
  return input
}

export function scopeSummary(scope: ProviderScope) {
  return `${scope.project} / ${scope.environments?.length ? scope.environments.join(', ') : 'All environments'}`
}

export function sameProviderSource(left: ProviderInput, right: SecretProvider) {
  return (
    [
      'name',
      'kind',
      'endpoint',
      'mount',
      'root_path',
      'project_id',
      'environment',
      'namespace',
      'ca_cert',
    ].every(
      (key) =>
        (left[key as keyof ProviderInput] || '') === (right[key as keyof SecretProvider] || ''),
    ) && JSON.stringify(left.private_cidrs || []) === JSON.stringify(right.private_cidrs || [])
  )
}

export function providerBinding(name: string) {
  return `DATABASE_URL = { provider = ${JSON.stringify(name || 'provider-name')}, path = "shop", key = "DATABASE_URL" }`
}
