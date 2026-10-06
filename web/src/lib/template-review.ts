import type { components } from './api.generated'

type SecretField = components['schemas']['TemplateSecretField']

export function templateReviewSecretFields(fields: readonly SecretField[], required: readonly string[]) {
  return required.map((name) => fields.find((field) => field.name === name) || {
    name,
    description: 'Save this value as an application secret. Only its reference belongs in the TOML.',
    format: 'password',
    generate: false,
    optional: false,
  })
}

export function templateReviewMatches(
  plan: components['schemas']['TemplatePlan'],
  expected: { name: string; project: string; environment: string; applicationId?: string; revision: number },
) {
  return plan.expected_revision === expected.revision &&
    plan.spec.name === expected.name &&
    plan.configuration.project === expected.project &&
    plan.configuration.environment === expected.environment &&
    (!expected.applicationId || plan.application_id === expected.applicationId)
}
