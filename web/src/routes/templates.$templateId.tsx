import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import TemplateForm from '../components/template-form'
import { ManagedActionsForm } from '../components/managed-actions-form'
import { FormPage } from '../components/form-page'
import { Empty, ErrorState, Loading, PageHeader } from '../components/shared'
import { ResourceEmpty } from '../components/resource-card'
import { Button } from '../components/ui/button'
import { client, unwrap } from '../lib/client'
import { useScope, useResourceScope } from '../lib/scope'

export const Route = createFileRoute('/templates/$templateId')({ component: ConfigureTemplate })
function ConfigureTemplate() {
  const { templateId } = Route.useParams()
  const { application: targetId, runner } = Route.useSearch()
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
      <div className="ops-page">
        <PageHeader title="Template not found" />
        <ResourceEmpty
          icon="search"
          title="This template is not in this installation's catalog"
          action={
            <Button asChild>
              <Link to="/templates" search={{ application: targetId }}>
                Back to catalog
              </Link>
            </Button>
          }
        />
      </div>
    )
  if (template.deployable && !scope.can('deployments:write'))
    return (
      <FormPage
        title={templateId === 'managed-actions' ? 'Runner pool setup' : 'Configure application'}
        description="Deployment access is required to continue."
        breadcrumbs={[]}
      >
        <Empty
          icon="lock"
          title="Deployment access required"
          description="Choose a project where you can create applications."
        />
      </FormPage>
    )
  if (templateId === 'managed-actions')
    return (
      <ManagedActionsForm
        key={`${targetId || 'new'}:${runner || 'new'}:${target.data?.project || scope.project}:${target.data?.environment || scope.environment}`}
        application={target.data}
        serviceName={runner}
        onClose={() => void navigate({ to: '/templates', search: { application: targetId } })}
      />
    )
  return (
    <TemplateForm
      key={`${template.id}:${targetId || 'new'}:${target.data?.project || scope.project}:${target.data?.environment || scope.environment}`}
      template={template}
      application={target.data}
      onClose={() => void navigate({ to: '/templates', search: { application: targetId } })}
    />
  )
}
