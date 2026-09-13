import type { components } from './api.generated'
import type { Spec } from './types'

export type VirtualNetworkSpec = components['schemas']['VirtualNetworkSpec']
export type VirtualNetworkDetail = components['schemas']['VirtualNetworkDetail']
export type VirtualNetworkPlan = components['schemas']['VirtualNetworkPlan']
export type SegmentDraft = { id: string; name: string; applications: string }

const validName = /^[a-z](?:[a-z0-9-]{0,38}[a-z0-9])?$/

export function segmentDrafts(spec?: VirtualNetworkSpec): SegmentDraft[] {
  return Object.entries(spec?.segments || { data: { applications: [] } }).map(
    ([name, segment]) => ({
      id: crypto.randomUUID(),
      name,
      applications: segment.applications.join('\n'),
    }),
  )
}

export function networkDraft(
  name: string,
  description: string,
  rows: SegmentDraft[],
): VirtualNetworkSpec {
  if (name === 'new') throw new Error('The name "new" is reserved. Choose another network name.')
  if (!validName.test(name))
    throw new Error(
      'Use a network name of 1–40 lowercase letters, numbers, or hyphens. Start with a letter and end with a letter or number.',
    )
  if (!rows.length || rows.length > 16) throw new Error('Define between 1 and 16 segments.')
  const segments: VirtualNetworkSpec['segments'] = Object.create(null)
  for (const row of rows) {
    if (!validName.test(row.name))
      throw new Error('Segment names follow the same rules as network names.')
    if (Object.hasOwn(segments, row.name))
      throw new Error(`Segment ${row.name} appears more than once.`)
    const applications = row.applications
      .split(/[,\n]/)
      .map((value) => value.trim())
      .filter(Boolean)
    if (
      applications.length > 64 ||
      applications.some((application) => !validName.test(application))
    )
      throw new Error(
        `Segment ${row.name} accepts up to 64 application names, one per line or separated by commas. Wildcards are not supported.`,
      )
    if (new Set(applications).size !== applications.length)
      throw new Error(`Segment ${row.name} has a duplicate application name.`)
    segments[row.name] = { applications }
  }
  return { schema_version: 1, name, description, segments }
}

export function networkTOML(spec: VirtualNetworkSpec) {
  return [
    'schema_version = 1',
    `name = ${JSON.stringify(spec.name)}`,
    `description = ${JSON.stringify(spec.description)}`,
    ...Object.entries(spec.segments).flatMap(([name, segment]) => [
      '',
      `[segments.${JSON.stringify(name)}]`,
      `applications = [${segment.applications.map((application) => JSON.stringify(application)).join(', ')}]`,
    ]),
    '',
  ].join('\n')
}

export function downloadNetworkTOML(name: string, toml: string) {
  const url = URL.createObjectURL(new Blob([toml], { type: 'application/toml' }))
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `${name}.network.toml`
  anchor.click()
  URL.revokeObjectURL(url)
}

export function connectVirtualNetwork(
  spec: Spec,
  network: VirtualNetworkSpec,
  segment: string,
  serviceName: string,
  localName: string,
): Spec {
  if (!validName.test(localName))
    throw new Error(
      'Use a local network name of 1–40 lowercase letters, numbers, or hyphens, starting with a letter and ending with a letter or number.',
    )
  if (!network.segments[segment]?.applications.includes(spec.name))
    throw new Error(
      'This application is not allowed to join the selected segment. Ask a project administrator to update its grants.',
    )
  if (!spec.services[serviceName])
    throw new Error('The selected service is no longer in this application.')
  const next = structuredClone(spec)
  const existing = next.networks?.[localName]
  if (
    existing &&
    (!existing.internal ||
      existing.virtual_network !== network.name ||
      existing.segment !== segment)
  )
    throw new Error(
      `${localName} is already a different application network. Choose an unused local name.`,
    )
  next.networks = {
    ...next.networks,
    [localName]: { internal: true, virtual_network: network.name, segment },
  }
  const memberships = next.services[serviceName].networks || ['default']
  if (memberships.includes(localName))
    throw new Error('This service is already connected through that local network.')
  next.services[serviceName].networks = [...memberships, localName]
  return next
}
