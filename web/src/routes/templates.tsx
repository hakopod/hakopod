import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope } from '../lib/scope'
import { specToTOML } from '../lib/toml'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Icon } from '../components/icons'
import { ErrorState, Loading, Note, PageHeader } from '../components/shared'
import { DiffTable } from '../components/deploy-dialog'
import { SecretForm } from '../components/application-secrets'

export const Route = createFileRoute('/templates')({ component: Templates })
function Templates() {
  const scope = useScope()
  const [selected, setSelected] = useState<components['schemas']['Template'] | null>(null)
  const templates = useQuery({
    queryKey: ['templates'],
    queryFn: ({ signal }) => unwrap(client.GET('/templates', { signal })),
    staleTime: 300000,
  })
  return (
    <>
      <PageHeader
        eyebrow="WORKSPACE / TEMPLATES"
        title="Start with something useful."
        description="Reviewed open-source applications for your own infrastructure."
      />
      {templates.isPending ? (
        <Loading />
      ) : templates.error ? (
        <ErrorState error={templates.error} retry={() => void templates.refetch()} />
      ) : (
        <div className="catalog-grid">
          {templates.data?.items.map((template) => (
            <article className="panel catalog-card" key={template.id}>
              <div className="title-row">
                <span className="app-symbol">
                  <Icon name={template.id === 'vllm' ? 'activity' : 'box'} size={24} />
                </span>
                <span className="label-chip">{template.category}</span>
              </div>
              <h2>{template.name}</h2>
              <p>{template.description}</p>
              <small className="muted-text">{template.license}</small>
              {template.requirements.length > 0 && (
                <ul className="field-help">
                  {template.requirements.map((requirement) => (
                    <li key={requirement}>{requirement}</li>
                  ))}
                </ul>
              )}
              <div className="toolbar-actions">
                <Button
                  variant="primary"
                  disabled={!scope.can('deployments:write')}
                  onClick={() => setSelected(template)}
                >
                  Configure template
                  <Icon name="arrow" size={14} />
                </Button>
                {/^https:\/\//.test(template.upstream) && (
                  <a
                    href={template.upstream}
                    target="_blank"
                    rel="noreferrer"
                    className="button button-ghost"
                  >
                    Upstream
                    <Icon name="external" size={14} />
                  </a>
                )}
              </div>
            </article>
          ))}
        </div>
      )}
      {selected && <TemplateDialog template={selected} onClose={() => setSelected(null)} />}
    </>
  )
}

function TemplateDialog({
  template,
  onClose,
}: {
  template: components['schemas']['Template']
  onClose: () => void
}) {
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [name, setName] = useState('')
  const [isPublic, setPublic] = useState(false)
  const [storage, setStorage] = useState(template.id === 'vllm' ? 30 : 5)
  const [model, setModel] = useState('')
  const [revision, setRevision] = useState('')
  const [plan, setPlan] = useState<components['schemas']['TemplatePlan'] | null>(null)
  const [key, setKey] = useState('')
  const [saveSecret, setSaveSecret] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const query = {
    project: scope.project,
    environment: scope.environment,
    application: plan?.spec.name || name,
  }
  const secrets = useQuery({
    queryKey: ['secrets', query.project, query.environment, query.application],
    queryFn: ({ signal }) => unwrap(client.GET('/secrets', { signal, params: { query } })),
    enabled: Boolean(plan?.required_secrets.length),
    gcTime: 0,
  })
  const missing =
    plan?.required_secrets.filter(
      (required) => !secrets.data?.items.some((secret) => secret.name === required),
    ) || []
  async function review() {
    setBusy(true)
    setError('')
    try {
      const result = await unwrap(
        client.POST('/templates/{id}/plan', {
          params: { path: { id: template.id } },
          body: {
            project: scope.project,
            environment: scope.environment,
            name,
            public: isPublic,
            storage_gib: storage,
            ...(template.id === 'vllm' ? { model, model_revision: revision } : {}),
          },
        }),
      )
      if (result.expected_revision !== 0)
        throw new Error(
          'An application with this name already exists. Choose a new name for this template.',
        )
      setPlan(result)
      setKey(crypto.randomUUID())
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  async function deploy() {
    if (!plan || missing.length || busy) return
    setBusy(true)
    setError('')
    try {
      const result = await unwrap(
        client.POST('/deployments', {
          params: { header: { 'Idempotency-Key': key } },
          body: {
            project: scope.project,
            environment: scope.environment,
            spec: plan.spec,
            expected_revision: plan.expected_revision,
          },
        }),
      )
      void cache.invalidateQueries({ queryKey: ['applications'] })
      onClose()
      void navigate({ to: '/deployments/$deploymentId', params: { deploymentId: result.id } })
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      open
      wide
      onOpenChange={(open) => {
        if (!busy && !open) onClose()
      }}
      title={plan ? `Review ${plan.spec.name}` : `Configure ${template.name}`}
      description={`${scope.project} / ${scope.environment} · ${plan ? 'Review the exact revision before deploying.' : template.description}`}
    >
      <div className="dialog-body auth-form">
        {plan ? (
          <>
            {plan.warnings.map((warning) => (
              <Note key={warning}>{warning}</Note>
            ))}
            <DiffTable changes={plan.changes} />
            {plan.model_source && (
              <Note>
                Model revision: <code>{plan.model_source}</code>. Model weights load in the
                workload, after deployment.
              </Note>
            )}
            {!!plan.required_secrets.length && (
              <section className="panel service-summary-panel">
                <h3>Required secrets</h3>
                {secrets.error && <ErrorState error={secrets.error} />}
                {plan.required_secrets.map((required) => (
                  <div className="settings-list-row" key={required}>
                    <div>
                      <strong className="mono">{required}</strong>
                      <small>
                        {missing.includes(required) ? 'Required before deployment' : 'Saved'}
                      </small>
                    </div>
                    <Button size="sm" onClick={() => setSaveSecret(required)}>
                      {missing.includes(required) ? 'Set value' : 'Replace'}
                    </Button>
                  </div>
                ))}
                {saveSecret && (
                  <SecretForm
                    key={saveSecret}
                    query={query}
                    initialName={saveSecret}
                    onSaved={() => {
                      setSaveSecret('')
                      void secrets.refetch()
                    }}
                  />
                )}
              </section>
            )}
            <details className="config-details">
              <summary>Canonical configuration</summary>
              <pre>{specToTOML(plan.spec)}</pre>
            </details>
          </>
        ) : (
          <>
            <label>
              Application name
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Choose a name"
                pattern="[a-z][a-z0-9-]*"
                maxLength={48}
                required
              />
            </label>
            <label>
              Persistent storage (GiB)
              <input
                type="number"
                min={1}
                max={1024}
                value={storage}
                onChange={(e) => setStorage(Number(e.target.value))}
              />
            </label>
            <label className="checkbox-row">
              <input
                type="checkbox"
                checked={isPublic}
                onChange={(e) => setPublic(e.target.checked)}
              />
              Expose supported HTTP service publicly
            </label>
            {template.id === 'vllm' && (
              <>
                <label>
                  Hugging Face model
                  <input
                    value={model}
                    onChange={(e) => setModel(e.target.value)}
                    placeholder="organization/model"
                    maxLength={200}
                  />
                </label>
                <label>
                  Model revision (optional)
                  <input
                    value={revision}
                    onChange={(e) => setRevision(e.target.value)}
                    placeholder="Resolve current immutable revision"
                    maxLength={64}
                  />
                </label>
                <Note>
                  Requires an NVIDIA GPU, compatible device plugin, storage, and enough GPU memory
                  for the selected model. Remote model code is disabled.
                </Note>
              </>
            )}
            {template.requirements.map((requirement) => (
              <Note key={requirement}>{requirement}</Note>
            ))}
          </>
        )}
        {error && (
          <div className="inline-error" role="alert">
            {error}
          </div>
        )}
      </div>
      <div className="dialog-footer">
        <Button disabled={busy} onClick={() => (plan ? setPlan(null) : onClose())}>
          {plan ? 'Back to configuration' : 'Cancel'}
        </Button>
        <Button
          variant="primary"
          disabled={
            busy ||
            !name ||
            !Number.isFinite(storage) ||
            storage < 1 ||
            (template.id === 'vllm' && !model) ||
            Boolean(
              plan &&
              (missing.length || saveSecret || (plan.required_secrets.length && secrets.isPending)),
            )
          }
          onClick={() => void (plan ? deploy() : review())}
        >
          {busy ? 'Working…' : plan ? 'Deploy template' : 'Review template'}
        </Button>
      </div>
    </Dialog>
  )
}
