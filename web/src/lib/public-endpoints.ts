import type { Application } from './types'
import type { components } from './api.generated'

export type DomainStatus = components['schemas']['Domain']
export type PublicEndpoint = {
  url: string
  service: string
  label: string
  custom: boolean
  status: string
}

function publicURL(value: string) {
  try {
    const url = new URL(value)
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) return null
    return url.href
  } catch {
    return null
  }
}

// Desired custom mappings are shown even before activation, with status kept separate.
export function publicEndpoints(
  application: Application,
  domains: DomainStatus[] = [],
  domainState: 'ready' | 'loading' | 'error' = 'ready',
): PublicEndpoint[] {
  const entries = new Map<string, PublicEndpoint>()
  const add = (endpoint: PublicEndpoint) => {
    const url = publicURL(endpoint.url)
    if (!url || !application.spec.services[endpoint.service]) return
    const key = endpoint.service + ':' + url
    if (!entries.has(key) || endpoint.custom) entries.set(key, { ...endpoint, url })
  }
  for (const [hostname, service] of Object.entries(application.spec.domains || {})) {
    // Only hostnames are valid here, never URL paths, credentials or query strings.
    if (
      !/^(?=.{1,253}$)(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/i.test(
        hostname,
      )
    )
      continue
    const domain = domains.find((item) => item.hostname === hostname && item.service === service)
    add({
      url: 'https://' + hostname,
      service,
      label: 'Custom domain',
      custom: true,
      status:
        domainState === 'error'
          ? 'Status unavailable'
          : domainState === 'loading'
            ? 'Checking status'
            : !domain
              ? 'Status unavailable'
              : domain.active
                ? 'Configured'
                : 'Needs setup',
    })
  }
  for (const service of application.observed?.services || []) {
    if (service.url)
      add({
        url: service.url,
        service: service.name,
        label: 'Generated endpoint',
        custom: false,
        status: 'Observed',
      })
    for (const [name, url] of Object.entries(service.endpoints || {})) {
      add({ url, service: service.name, label: name, custom: false, status: 'Observed' })
    }
  }
  return [...entries.values()].sort(
    (a, b) =>
      Number(b.custom) - Number(a.custom) ||
      a.service.localeCompare(b.service) ||
      a.url.localeCompare(b.url),
  )
}
