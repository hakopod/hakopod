import type { Service } from './types'

type Actions = NonNullable<Service['actions']>

export function actionsProvider(actions: Actions) {
  return (
    actions.provider || (actions.gitlab ? 'gitlab' : actions.bitbucket ? 'bitbucket' : 'github')
  )
}

export function actionsProviderName(actions: Actions) {
  const provider = actions.gitlab
    ? 'gitlab'
    : actions.bitbucket
      ? 'bitbucket'
      : actionsProvider(actions)
  if (provider === 'github') return 'GitHub'
  if (provider === 'gitlab') return 'GitLab'
  if (provider === 'bitbucket') return 'Bitbucket'
  return provider
}

export function actionsNeedTOML(actions: Actions) {
  // Preserve imported targets even when their provider discriminator is invalid.
  return actionsProvider(actions) !== 'github' || !!actions.gitlab || !!actions.bitbucket
}

export function actionsTargetLabel(actions: Actions) {
  if (actions.gitlab)
    return `${actions.gitlab.url || 'https://gitlab.com'} / ${actions.gitlab.project_id ? `project ${actions.gitlab.project_id}` : `group ${actions.gitlab.group_id}`}`
  if (actions.bitbucket)
    return [actions.bitbucket.workspace, actions.bitbucket.repository].filter(Boolean).join(' / ')
  return actions.organization || actions.repository || actionsProviderName(actions)
}

export function actionsRemovalMessage(actions: Actions) {
  const provider = actionsProviderName(actions)
  const effect =
    actionsProvider(actions) === 'github'
      ? 'cancels running GitHub jobs and removes its runner registrations'
      : 'stops new jobs, lets running jobs finish, and removes its runner registrations'
  return `Deleting this pool ${effect}. Cleanup continues automatically if ${provider} is temporarily unavailable.`
}
