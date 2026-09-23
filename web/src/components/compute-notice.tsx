import { useEditionFeatures } from '../lib/dashboard-edition'
import { Note } from './shared'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'

export function ComputeNotice({ creatingApplication = false }: { creatingApplication?: boolean }) {
  const features = useEditionFeatures()
  const scope = useScope()
  const applications = useQuery({
    queryKey: ['applications', scope.project, scope.environment, ''],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications', {
          signal,
          params: {
            query: {
              project: scope.project,
              environment: scope.environment,
              cursor: '',
              limit: 25,
            },
          },
        }),
      ),
    enabled: Boolean(
      features.hostedCompute && creatingApplication && scope.project && scope.environment,
    ),
    gcTime: 0,
  })
  if (!features.hostedCompute) return null
  return (
    <Note>
      <strong>{features.hostedFree ? 'Hosted Free limits' : 'Hosted compute limits'}</strong>
      <p>
        {features.hostedFree
          ? 'One application with one small service and one replica.'
          : 'One application with up to ten services and three replicas per service within your reserved memory budget.'}{' '}
        Files on its container disk are temporary. Persistent storage, scheduled jobs and public TCP
        ports require your own server. Images must support AMD64. Outbound connections are limited
        to HTTP and HTTPS; databases and queues on other ports need your own server. Native
        application secrets are supported. External secret providers, custom networking and
        autoscaling require your own server.
      </p>
      {creatingApplication && applications.data?.items.length ? (
        <p>
          Your application slot is in use.{' '}
          <a href={`/applications/${applications.data.items[0].id}`}>
            Open {applications.data.items[0].name}
          </a>{' '}
          to configure its service or source build. A second application requires your own server.
        </p>
      ) : null}
      <a href={features.computeURL} target="_blank" rel="noopener noreferrer">
        View compute options in a new tab
      </a>
    </Note>
  )
}
