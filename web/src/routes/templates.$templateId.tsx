import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import TemplateForm from '../components/template-form'
import { Empty, ErrorState, Loading } from '../components/shared'
import { client, unwrap } from '../lib/client'
import { useScope, useResourceScope } from '../lib/scope'

export const Route = createFileRoute('/templates/$templateId')({ component: ConfigureTemplate })
function ConfigureTemplate() {
  const { templateId } = Route.useParams()
  const { application: targetId } = Route.useSearch()
  const target = useQuery({
    queryKey: ['application', targetId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: targetId! } } })),
    enabled: !!targetId,
    gcTime: 0,
  })
  useResourceScope(target.data)
  const navigate = useNavigate()
  const scope = useScope()
  const templates = useQuery({
    queryKey: ['templates'],
    queryFn: ({ signal }) => unwrap(client.GET('/templates', { signal })),
    staleTime: 300000,
  })
  if (targetId && target.isPending) return <Loading />
  if (targetId && (target.error || !target.data)) return <ErrorState error={target.error} />
  if (templates.isPending) return <Loading />
  if (templates.error) return <ErrorState error={templates.error} />
  const template = templates.data?.items.find((item) => item.id === templateId)
  if (!template)
    return (
      <Empty
        title="Template not found"
        description="This template is absent from the current installation catalog."
      />
    )
  if (template.deployable && !scope.can('deployments:write'))
    return (
      <Empty
        icon="lock"
        title="Deployment access required"
        description="Choose a project where you can create applications."
      />
    )
  return (
    <TemplateForm
      key={`${template.id}:${scope.project}:${scope.environment}`}
      template={template}
      application={target.data}
      onClose={() => void navigate({ to: '/templates', search: { application: targetId } })}
    />
  )
}
