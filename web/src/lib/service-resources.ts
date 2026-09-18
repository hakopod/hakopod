import type { Plan, Service } from './types'

export function serviceResources(service: Service, profiles: Plan['resource_profiles']) {
  const defaults = profiles?.[service.size || 'small']
  if (!defaults) return undefined
  return {
    CPURequest: service.resources?.cpu_request || defaults.CPURequest,
    CPULimit: service.resources?.cpu_limit || defaults.CPULimit,
    MemoryRequest: service.resources?.memory_request || defaults.MemoryRequest,
    MemoryLimit: service.resources?.memory_limit || defaults.MemoryLimit,
  }
}

// Size remains the source of defaults for fields without an override. Summaries
// describe the effective configuration, including partial explicit overrides.
export function serviceProfileLabel(service: Service) {
  return Object.values(service.resources || {}).some((value) => value?.trim())
    ? 'Custom'
    : service.size || 'small'
}
