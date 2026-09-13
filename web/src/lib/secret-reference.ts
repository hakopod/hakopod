import type { components } from './api.generated'

export function secretReference(reference: components['schemas']['SecretRef']): string {
  return reference.ref || `${reference.provider}/${reference.path || ''}:${reference.key}`
}
