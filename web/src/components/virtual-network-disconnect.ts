import type { Spec } from '../lib/types'

// The reverse of connectVirtualNetwork: drop one service membership, and the local
// network declaration once no service uses it.
export function disconnectVirtualNetwork(spec: Spec, network: string, serviceName: string, local: string) {
  const next = structuredClone(spec)
  const service = next.services[serviceName]
  if (!service) throw new Error('The service is no longer in this application.')
  const memberships = service.networks || ['default']
  if (!memberships.includes(local))
    throw new Error(`${serviceName} is no longer connected through ${local}.`)
  const remaining = memberships.filter((name) => name !== local)
  if (!remaining.length)
    throw new Error(
      `${local} is the only network of ${serviceName}. Add another network to it in application TOML before disconnecting.`,
    )
  service.networks = remaining
  const unused = !Object.values(next.services).some((item) =>
    (item.networks || ['default']).includes(local),
  )
  let removedLocal = false
  if (unused && next.networks?.[local]?.virtual_network === network) {
    const { [local]: _removed, ...rest } = next.networks
    next.networks = rest
    removedLocal = true
  }
  return { spec: next, removedLocal }
}
