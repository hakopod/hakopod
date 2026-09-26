import { fieldError, validationLines } from './form-errors'

export const buildSteps = ['Repository', 'Build recipe', 'Runtime', 'Review'] as const

// Only explicit API paths move the user back to a field. Permissions, network
// failures and capacity errors stay on the current step with a recovery banner.
export function buildErrorStep(error: string, service: string): number | undefined {
  const fields = [
    [
      'name',
      'application',
      'service',
      'provider',
      'connection_id',
      'repository',
      'branch',
      'context_path',
      'submodules',
    ],
    ['mode', 'preset', 'architecture', 'dockerfile', 'build_args', 'build_secrets', 'framework'],
    ['port', 'size', 'command', 'args', 'env', 'secrets', 'registry_credential', 'reuse_services'],
  ]
  for (let step = 0; step < fields.length; step++) {
    for (const path of fields[step]) {
      if (fieldError(error, path, `services.${service}.${path}`)) return step
      if (
        validationLines(error).some(
          (line) =>
            (line.startsWith(`${path}.`) || line.startsWith(`services.${service}.${path}.`)) &&
            line.includes(':'),
        )
      )
        return step
    }
  }
}

// Opening an advanced group before native validation keeps its invalid input
// focusable. Disabled fieldsets retain drafts without validating other steps.
export function validateVisibleFields(form: HTMLFormElement) {
  for (const control of form.querySelectorAll<
    HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement
  >('input:not(:disabled), textarea:not(:disabled), select:not(:disabled)')) {
    if (!control.willValidate || control.validity.valid) continue
    let details = control.closest('details')
    while (details) {
      details.open = true
      details = details.parentElement?.closest('details') || null
    }
    control.reportValidity()
    return false
  }
  return true
}
