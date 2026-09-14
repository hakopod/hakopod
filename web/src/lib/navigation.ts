export type ParentNavigation = {
  to: string
  label: string
  search?: Record<string, string>
}

export function parentNavigation(
  pathname: string,
  search: Record<string, unknown> = {},
  deploymentApplication?: string,
  scope?: { project: string; environment: string },
): ParentNavigation | null {
  const parts = pathname.split('/').filter(Boolean)
  const applications: ParentNavigation = scope?.project
    ? {
        to: `/projects/${encodeURIComponent(scope.project)}`,
        label: 'Back to applications',
        search: scope.environment ? { environment: scope.environment } : {},
      }
    : { to: '/', label: 'Back to projects' }
  const service = typeof search.service === 'string' ? search.service : ''
  const application = (id: string, tab: string, includeService = false): ParentNavigation => ({
    to: `/applications/${id}`,
    label: includeService && service ? 'Back to service' : 'Back to application',
    search: { tab, ...(includeService && service ? { service } : {}) },
  })
  if (parts[0] === 'alarms' && parts[1])
    return {
      to: '/alarms',
      label: 'Back to alarms',
      search: Object.fromEntries(
        ['project', 'environment', 'application_id'].flatMap((key) =>
          typeof search[key] === 'string' ? [[key, search[key] as string]] : [],
        ),
      ),
    }
  if (parts[0] === 'projects') return { to: '/', label: 'Back to projects' }
  if (parts[0] === 'applications' && parts[1]) {
    if (['new', 'import'].includes(parts[1])) return applications
    if (parts[2]) {
      if (parts[2] === 'source') return application(parts[1], 'source')
      const tab = ['domains', 'certificates'].includes(parts[2])
        ? service
          ? 'network'
          : 'networking'
        : service
          ? parts[2] === 'environment'
            ? 'environment'
            : 'settings'
          : 'configuration'
      return application(parts[1], tab, true)
    }
    return service ? application(parts[1], 'services') : applications
  }
  if (parts[0] === 'deployments' && parts[1])
    return deploymentApplication
      ? application(encodeURIComponent(deploymentApplication), 'deployments')
      : applications
  if (parts[0] === 'builds' && parts[1]) {
    if (parts[2] === 'edit') return { to: `/builds/${parts[1]}`, label: 'Back to build' }
    if (parts[1] === 'new' && typeof search.application === 'string' && search.application)
      return application(encodeURIComponent(search.application), 'source')
    return { to: '/builds', label: 'Back to builds' }
  }
  if (parts[0] === 'backups' && parts[1]) {
    const tab = ['destinations', 'schedules', 'artifacts'].includes(parts[1]) ? parts[1] : 'jobs'
    return { to: '/backups', search: { tab }, label: 'Back to backups' }
  }
  if (parts[0] === 'infrastructure' && parts[1])
    return {
      to: '/infrastructure',
      search: { tab: parts[1] === 'registries' ? 'registries' : 'nodes' },
      label: 'Back to infrastructure',
    }
  if (parts[0] === 'settings' && parts[1]) {
    if (parts[1] === 'git')
      return { to: '/settings', search: { tab: 'github' }, label: 'Back to Git connections' }
    if (parts[1] === 'login-providers' || parts[1] === 'smtp')
      return { to: '/settings', search: { tab: parts[1] }, label: 'Back to settings' }
    if (parts[1] === 'secret-providers' && parts[2])
      return { to: '/settings/secret-providers', label: 'Back to providers' }
    if (parts[1] === 'integrations' && parts[2])
      return { to: '/settings/integrations', label: 'Back to integrations' }
    return {
      to: '/settings',
      search: {
        tab:
          parts[1] === 'secret-providers'
            ? 'secret-providers'
            : parts[1] === 'integrations'
              ? 'github'
              : parts[1] === 'host-access'
                ? 'users'
                : 'account',
      },
      label: 'Back to settings',
    }
  }
  if (parts[0] === 'networks' && parts[1])
    return parts[2] === 'connect'
      ? { to: `/networks/${parts[1]}`, label: 'Back to network' }
      : { to: '/networks', label: 'Back to networks' }
  if (parts[0] === 'templates' && parts[1])
    return {
      to: '/templates',
      label: 'Back to catalog',
      search: Object.fromEntries(
        ['q', 'category'].flatMap((key) =>
          typeof search[key] === 'string' ? [[key, search[key]]] : [],
        ),
      ),
    }
  if (parts[0] === 'login' && parts[1]) return { to: '/', label: 'Back to workspace' }
  return null
}
