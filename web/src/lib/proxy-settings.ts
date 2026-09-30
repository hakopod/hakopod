import type { components } from './api.generated'

export type ProxyStatus = components['schemas']['ProxyStatus']
export type EdgePolicy = components['schemas']['EdgePolicy']
export type EdgeRule = components['schemas']['EdgeRule']

export type EdgeRuleDraft = Omit<
  EdgeRule,
  'allow_cidrs' | 'deny_cidrs' | 'allow_countries' | 'deny_countries' | 'requests_per_second'
> & {
  key: string
  allow_cidrs: string
  deny_cidrs: string
  allow_countries: string
  deny_countries: string
  requests_per_second: string
}
export type EdgeDraft = Omit<
  EdgePolicy,
  'rules' | 'trusted_proxy_cidrs' | 'client_ip_header' | 'country_header'
> & {
  trusted_proxy_cidrs: string
  client_ip_header: NonNullable<EdgePolicy['client_ip_header']>
  country_header: NonNullable<EdgePolicy['country_header']>
  rules: EdgeRuleDraft[]
}

export function edgeDraft(policy: EdgePolicy): EdgeDraft {
  return {
    ...policy,
    trusted_proxy_cidrs: (policy.trusted_proxy_cidrs || []).join('\n'),
    client_ip_header: policy.client_ip_header || '',
    country_header: policy.country_header || '',
    rules: (policy.rules || []).map((rule, index) => ({
      ...rule,
      key: `stored-${index}`,
      path_prefix: rule.path_prefix || '/',
      allow_cidrs: (rule.allow_cidrs || []).join('\n'),
      deny_cidrs: (rule.deny_cidrs || []).join('\n'),
      allow_countries: (rule.allow_countries || []).join(', '),
      deny_countries: (rule.deny_countries || []).join(', '),
      requests_per_second: String(rule.requests_per_second || 0),
    })),
  }
}

export function newEdgeRule(rules: EdgeRuleDraft[]): EdgeRuleDraft {
  let suffix = 1
  while (rules.some((rule) => rule.id === `rule-${suffix}`)) suffix++
  return {
    key: crypto.randomUUID(),
    id: `rule-${suffix}`,
    host: '',
    path_prefix: '/',
    allow_cidrs: '',
    deny_cidrs: '',
    allow_countries: '',
    deny_countries: '',
    requests_per_second: '0',
  }
}

function entries(value: string, maximum: number, label: string): string[] {
  const values = value.split(/[\s,]+/).filter(Boolean)
  if (values.length > maximum) throw new Error(`${label} accepts at most ${maximum} entries.`)
  return values
}

function validNetwork(value: string): boolean {
  const parts = value.split('/')
  if (parts.length > 2) return false
  const address = parts[0]
  const ipv6 = address.includes(':')
  if (ipv6) {
    if (!/^[0-9a-fA-F:.]+$/.test(address)) return false
    try {
      const parsed = new URL(`http://[${address}]/`)
      if (parsed.hostname.toLowerCase().startsWith('[::ffff:')) return false
    } catch {
      return false
    }
  } else if (
    !/^(?:0|[1-9]\d{0,2})(?:\.(?:0|[1-9]\d{0,2})){3}$/.test(address) ||
    address.split('.').some((part) => Number(part) > 255)
  )
    return false
  return (
    parts.length === 1 ||
    (/^(?:0|[1-9]\d{0,2})$/.test(parts[1]) && Number(parts[1]) <= (ipv6 ? 128 : 32))
  )
}

function networks(value: string, maximum: number, label: string, trusted = false): string[] {
  const values = entries(value, maximum, label)
  if (values.some((item) => !validNetwork(item)))
    throw new Error(`${label} must contain IPv4 or IPv6 addresses or CIDR networks.`)
  if (trusted && values.some((item) => item.includes('/') && Number(item.split('/')[1]) === 0))
    throw new Error('Trusted proxy networks cannot include the entire internet (/0).')
  return values
}

function countries(value: string, label: string): string[] {
  const values = entries(value.toUpperCase(), 64, label)
  if (values.some((item) => !/^[A-Z]{2}$/.test(item)))
    throw new Error(`${label} must contain two-letter country codes, such as US or DE.`)
  return values
}

export function prepareEdgePolicy(draft: EdgeDraft): EdgePolicy {
  if (draft.rules.length > 32) throw new Error('Use at most 32 traffic rules.')
  const trusted =
    draft.client_ip_source === 'trusted_proxy'
      ? networks(draft.trusted_proxy_cidrs, 32, 'Trusted proxy networks', true)
      : []
  if (draft.client_ip_source === 'trusted_proxy' && (!trusted.length || !draft.client_ip_header))
    throw new Error('Choose a client IP header and at least one trusted proxy network.')
  const identifiers = new Set<string>()
  const selectors = new Set<string>()
  let networkCount = trusted.length
  const rules: EdgeRule[] = draft.rules.map((rule, index) => {
    const label = `Rule ${index + 1}`
    const id = rule.id.trim()
    if (!/^[a-z0-9][a-z0-9_-]{0,31}$/.test(id) || identifiers.has(id))
      throw new Error(
        `${label} needs a unique ID using up to 32 lowercase letters, digits, - or _.`,
      )
    identifiers.add(id)
    const host = rule.host.trim().toLowerCase()
    if (
      !host ||
      host.length > 253 ||
      host.split('.').some((part) => !/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(part))
    )
      throw new Error(`${label} needs an exact hostname without a scheme, port or wildcard.`)
    const path = rule.path_prefix.trim() || '/'
    if (
      !/^\/[a-zA-Z0-9/._~-]*$/.test(path) ||
      path.length > 128 ||
      path.includes('//') ||
      path.split('/').some((part) => part === '.' || part === '..')
    )
      throw new Error(
        `${label} needs a path of up to 128 letters, digits, /, ., _, ~ or -. Empty and dot segments are not allowed.`,
      )
    const selector = `${host}${path}`
    if (selectors.has(selector)) throw new Error(`${label} repeats an existing hostname and path.`)
    selectors.add(selector)
    if (!/^\d+$/.test(rule.requests_per_second) || Number(rule.requests_per_second) > 100000)
      throw new Error(`${label} needs a request limit from 0 to 100000. Use 0 for no rate limit.`)
    const allow = networks(rule.allow_cidrs, 64, `${label} allowed IPs`)
    const deny = networks(rule.deny_cidrs, 64, `${label} denied IPs`)
    networkCount += allow.length + deny.length
    const allowCountries = countries(rule.allow_countries, `${label} allowed countries`)
    const denyCountries = countries(rule.deny_countries, `${label} denied countries`)
    if (
      (allowCountries.length || denyCountries.length) &&
      (draft.client_ip_source !== 'trusted_proxy' || !draft.country_header)
    )
      throw new Error(`${label} country rules need a trusted proxy and a country header.`)
    return {
      id,
      host,
      path_prefix: path,
      allow_cidrs: allow,
      deny_cidrs: deny,
      allow_countries: allowCountries,
      deny_countries: denyCountries,
      requests_per_second: Number(rule.requests_per_second),
    }
  })
  if (networkCount > 512) throw new Error('Use at most 512 IP addresses or networks in total.')
  return {
    enabled: draft.enabled,
    client_ip_source: draft.client_ip_source,
    trusted_proxy_cidrs: trusted,
    client_ip_header: draft.client_ip_source === 'trusted_proxy' ? draft.client_ip_header : '',
    country_header: draft.client_ip_source === 'trusted_proxy' ? draft.country_header : '',
    rules,
  }
}

export function sameEdgePolicy(left: EdgePolicy, right: EdgePolicy): boolean {
  return (
    JSON.stringify(prepareEdgePolicy(edgeDraft(left))) ===
    JSON.stringify(prepareEdgePolicy(edgeDraft(right)))
  )
}

export function editableProxySettings(snapshot: ProxyStatus): Record<string, string> {
  return Object.fromEntries(
    snapshot.observed.fields
      .filter((field) => field.name in snapshot.observed.settings)
      .map((field) => [field.name, snapshot.observed.settings[field.name]]),
  )
}

export function prepareProxySettings(text: string, snapshot: ProxyStatus): Record<string, string> {
  let values: unknown
  try {
    values = JSON.parse(text)
  } catch {
    throw new Error('Enter a valid JSON object for the controller settings.')
  }
  if (
    !values ||
    Array.isArray(values) ||
    typeof values !== 'object' ||
    Object.values(values).some((value) => typeof value !== 'string')
  )
    throw new Error('Controller settings must be a JSON object with string values.')

  const supported = new Set(snapshot.observed.fields.map((field) => field.name))
  for (const [name, value] of Object.entries(values)) {
    if (!supported.has(name)) throw new Error(`Unsupported controller setting: ${name}.`)
    if (value.length > 32 || value.trim() !== value)
      throw new Error(`Use at most 32 characters without surrounding spaces for ${name}.`)
  }
  return Object.fromEntries(
    Object.entries(values as Record<string, string>).filter(
      ([name, value]) => value !== (snapshot.observed.settings[name] || ''),
    ),
  )
}
