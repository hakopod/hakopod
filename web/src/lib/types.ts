import type { components } from './api.generated'
// One schema owns CLI, server, and dashboard types. Regenerate with pnpm generate:api.
export type Identity = components['schemas']['Principal']
export type Project = components['schemas']['Project']
export type Service = components['schemas']['Service']
export type Spec = components['schemas']['Spec']
export type ServiceStatus = components['schemas']['ServiceStatus']
export type Application = components['schemas']['Application']
export type Deployment = components['schemas']['Deployment']
export type Node = components['schemas']['Node']
export type Plan = components['schemas']['Plan']
export type Accepted = Deployment
export type APIKey = components['schemas']['Key']
export type DeploymentSummary = NonNullable<Application['deployments']>[number]
