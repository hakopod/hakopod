import type { Spec } from './types'

// Advisory feedback for the form. The server still applies the workspace's
// authoritative policy to imported TOML, saved builds and every deployment.
export function hostedFreeIssues(spec: Spec): string[] {
  return hostedComputeIssues(spec, true)
}

export function hostedComputeIssues(spec: Spec, free = false): string[] {
  const issues: string[] = []
  if (Object.keys(spec.services).length > (free ? 1 : 10))
    issues.push(
      free
        ? 'services: Hosted Free supports one service. Connect your own server for multiple services.'
        : 'services: Hosted compute supports up to ten services.',
    )
  if (Object.keys(spec.volumes || {}).length)
    issues.push('volumes: Persistent storage requires your own server.')
  if (Object.values(spec.networks || {}).some((network) => network.virtual_network))
    issues.push('networks: Shared virtual networks require your own server.')
  for (const [name, service] of Object.entries(spec.services)) {
    const add = (field: string, text: string) => issues.push(`services.${name}.${field}: ${text}`)
    if (free && service.size && service.size !== 'small')
      add(
        'size',
        'Hosted Free supports the small profile. Connect your own server for larger sizes.',
      )
    if ((service.replicas ?? 1) > (free ? 1 : 3))
      add(
        'replicas',
        free
          ? 'Hosted Free supports one replica.'
          : 'Hosted compute supports up to three replicas.',
      )
    if (service.architecture && service.architecture !== 'amd64')
      add('architecture', 'Hosted compute runs AMD64 images.')
    for (const [field, unsupported] of Object.entries({
      resources: free && Object.values(service.resources || {}).some(Boolean),
      node_name: Boolean(service.node_name),
      serverless: Boolean(service.serverless),
      private_egress: Boolean(service.private_egress?.length),
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
          'Use native application secrets on hosted compute. External providers require your own server.',
        )
        break
      }
  }
  return issues
}
