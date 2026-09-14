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
