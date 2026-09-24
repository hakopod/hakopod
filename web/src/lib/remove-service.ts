import type { Spec } from './types'

// Domain mappings cannot outlive their service. Dependencies and peer rules
// stay explicit: validation points out references the user must review.
export function withoutService(input: Spec, name: string): Spec {
  const next = structuredClone(input)
  delete next.services[name]
  if (next.domains) {
    next.domains = Object.fromEntries(
      Object.entries(next.domains).filter(([, service]) => service !== name),
    )
  }
  return next
}

// Shared named volumes stay attached to remaining services. Only definitions
// unused after this removal are eligible for the explicit data-deletion option.
export function serviceVolumeRemoval(input: Spec, name: string) {
  const spec = withoutService(input, name)
  const claims: string[] = []
  const service = input.services[name]
  if (service?.volume) claims.push(`${name}-data`)
  for (const mount of service?.mounts || []) {
    if (Object.values(spec.services).some((other) => other.mounts?.some((m) => m.volume === mount.volume))) continue
    if (spec.volumes) delete spec.volumes[mount.volume]
    const claim = `hakopod-volume-${mount.volume}`
    if (!claims.includes(claim)) claims.push(claim)
  }
  return { spec, claims }
}
