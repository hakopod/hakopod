import type { Spec } from './types'

// Advisory feedback for the form. The server still applies the workspace's
// authoritative policy to imported TOML, saved builds and every deployment.
export function hostedFreeIssues(spec: Spec): string[] {
  const issues: string[] = []
  if (Object.keys(spec.services).length > 1)
    issues.push(
      'services: Hosted Free supports one service. Connect your own server for multiple services.',
    )
  if (Object.keys(spec.volumes || {}).length)
    issues.push('volumes: Persistent storage requires your own server.')
  if (Object.values(spec.networks || {}).some((network) => network.virtual_network))
    issues.push('networks: Shared virtual networks require your own server.')
  for (const [name, service] of Object.entries(spec.services)) {
    const add = (field: string, text: string) => issues.push(`services.${name}.${field}: ${text}`)
    if (service.size && service.size !== 'small')
      add(
        'size',
        'Hosted Free supports the small profile. Connect your own server for larger sizes.',
      )
    if ((service.replicas ?? 1) > 1) add('replicas', 'Hosted Free supports one replica.')
    if (service.architecture && service.architecture !== 'amd64')
      add('architecture', 'Hosted Free runs AMD64 images.')
    for (const [field, unsupported] of Object.entries({
      job: Boolean(service.job),
      autoscaling: Boolean(service.autoscaling),
      volume: Boolean(service.volume),
      mounts: Boolean(service.mounts?.length),
      public_tcp: Boolean(service.public_tcp?.length),
      certificate_mounts: Boolean(service.certificate_mounts?.length),
      aws_identity: Boolean(service.aws_identity),
      gpu: Boolean(service.gpu),
      bindings: Boolean(service.bindings?.length),
      network_access: Boolean(service.network_access),
      readiness: Boolean(service.readiness),
    }))
      if (unsupported)
        add(field, 'This setting requires your own server. Your configuration has been kept.')
    for (const ref of Object.values({
      ...(spec.inject_env ? spec.secrets : {}),
      ...service.secrets,
    }))
      if (!ref.ref || ref.provider) {
        add(
          'secrets',
          'Use native application secrets on hosted Free. External providers require your own server.',
        )
        break
      }
  }
  return issues
}
