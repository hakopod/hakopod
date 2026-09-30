import type { components } from './api.generated'

export type TLSIssuer = components['schemas']['TLSIssuer']
export type TLSIssuers = components['schemas']['TLSIssuers']
export type TLSReference = components['schemas']['TLSConfig']
export type TLSMethod = 'upload' | 'issuer'

export function tlsIssuersQueryKey(applicationID?: string) {
  return applicationID === undefined
    ? ['tls-issuers']
    : ['tls-issuers', 'application', applicationID]
}

export function tlsIssuerKey(issuer: Pick<TLSIssuer, 'name' | 'kind'>) {
  return `${issuer.kind}:${issuer.name}`
}

export function tlsIssuerScope(issuer: Pick<TLSIssuer, 'kind' | 'default'>) {
  if (issuer.default) return 'Default issuer'
  return issuer.kind === 'Issuer' ? 'Application issuer' : 'Installation issuer'
}

export function preferredTLSIssuer(
  items: TLSIssuer[],
  selected: string | undefined,
  current?: TLSReference,
) {
  if (selected !== undefined) return selected
  if (current?.issuer)
    return tlsIssuerKey({ name: current.issuer, kind: current.issuer_kind || 'ClusterIssuer' })
  const defaultIssuer = items.find((item) => item.default)
  return defaultIssuer ? tlsIssuerKey(defaultIssuer) : ''
}

export function findTLSIssuer(items: TLSIssuer[], selected: string) {
  return items.find((item) => tlsIssuerKey(item) === selected)
}

export function preferredTLSMethod(
  selected: TLSMethod | undefined,
  current: TLSReference | undefined,
  cloud: boolean,
): TLSMethod {
  if (selected) return selected
  if (current?.issuer) return 'issuer'
  if (current?.certificate) return 'upload'
  return cloud ? 'issuer' : 'upload'
}
