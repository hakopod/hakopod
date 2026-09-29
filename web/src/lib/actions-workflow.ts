import type { components } from './api.generated'

type Job = components['schemas']['ActionsJob']

export function workflowJobPresentation(item: Job) {
  const provider = item.provider || 'github'
  const providerName =
    provider === 'gitlab' ? 'GitLab' : provider === 'bitbucket' ? 'Bitbucket' : 'GitHub'
  const native = provider !== 'github'
  const details = native ? item.native_job : item.job
  const identity = native ? item.native_job?.identity : undefined
  const discoveryMessage = !native
    ? ''
    : item.discovery_state === 'reuse_detected'
      ? 'This runner processed more than one job. The pool is held for operator inspection.'
      : item.discovery_state === 'unavailable'
        ? 'The job assignment could not be verified before runner cleanup. This does not mean that no job ran.'
        : !details
          ? `Waiting for ${providerName} to identify the job assigned to this runner.`
          : ''
  const status =
    native && item.discovery_state === 'reuse_detected'
      ? 'reuse_detected'
      : details?.conclusion ||
        details?.status ||
        (native && item.discovery_state === 'unavailable' ? 'unavailable' : 'waiting')
  const name = details?.name || (native ? 'Job assignment' : item.observation.job_key)
  if (native) {
    const title = identity ? `Pipeline #${identity.run_id}` : 'Job assignment'
    const context = identity
      ? `${providerName} · Project ${identity.repository}`
      : `${providerName} · Runner ${item.provider_runner_id || `hakopod-${item.slot_id}`}`
    return {
      providerName,
      native,
      details,
      status,
      name,
      discoveryMessage,
      // The public history does not expose its original instance. Keep each
      // native slot distinct instead of merging equal IDs from different hosts.
      runKey: `${provider}:slot:${item.slot_id}`,
      title,
      context,
      option: identity
        ? `${title} · ${name} · Project ${identity.repository}`
        : `${title} · ${item.provider_runner_id || item.slot_id}`,
      href: undefined,
      jobID: identity?.job_id,
    }
  }
  const observation = item.observation
  return {
    providerName,
    native,
    details,
    status,
    name,
    discoveryMessage,
    runKey: `${observation.repository}:${observation.run_id}:${observation.attempt}`,
    title: `${observation.workflow} #${observation.run_number}`,
    context: `${observation.repository} · ${observation.branch} · ${observation.sha.slice(0, 7)} · Attempt ${observation.attempt}`,
    option: `${observation.workflow} #${observation.run_number} · Attempt ${observation.attempt} · ${observation.repository}`,
    href: `https://github.com/${observation.repository}/actions/runs/${observation.run_id}/attempts/${observation.attempt}`,
    jobID: undefined,
  }
}
