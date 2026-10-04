import type { components } from './api.generated'

type TemplateConfigField = components['schemas']['TemplateConfigField']

export function activeTemplateConfigFields(
  fields: readonly TemplateConfigField[],
  values: Readonly<Record<string, string>>,
) {
  return fields.filter((field) => !field.when || values[field.when.field] === field.when.value)
}

export function activeTemplateConfigValues(
  fields: readonly TemplateConfigField[],
  values: Readonly<Record<string, string>>,
) {
  return Object.fromEntries(
    activeTemplateConfigFields(fields, values).map((field) => [
      field.name,
      values[field.name] || '',
    ]),
  )
}

export function unsupportedHostedTemplateRequirements(
  template: Pick<components['schemas']['Template'], 'id' | 'workload_requirements'>,
  features: { hostedStorageGiB: number; hostedFree: boolean },
) {
  return (template.workload_requirements || []).filter((requirement) => {
    if (requirement === 'persistent_storage' && features.hostedStorageGiB > 0) return false
    if (
      ['larger_service', 'multiple_services', 'multiple_replicas'].includes(requirement) &&
      !features.hostedFree
    )
      return false
    if (
      requirement === 'larger_service' &&
      features.hostedFree &&
      ['postgresql', 'redis', 'mysql'].includes(template.id)
    )
      return false
    return true
  })
}

export const workloadRequirementLabel: Record<string, string> = {
  persistent_storage: 'Persistent storage',
  shared_storage: 'Shared storage (ReadWriteMany)',
  multiple_services: 'Multiple services',
  larger_service: 'A larger service size',
  multiple_replicas: 'Multiple replicas',
  jobs: 'Jobs or scheduled tasks',
  autoscaling: 'Autoscaling',
  public_tcp: 'Public TCP ports',
  certificate_mounts: 'Mounted certificates',
  cloud_identity: 'Cloud provider identity',
  gpu: 'GPU compute',
  service_bindings: 'Service bindings',
  custom_networking: 'Custom networking',
  custom_readiness: 'Custom readiness probes',
  external_secrets: 'External secret providers',
  virtual_networks: 'Virtual networks',
}
