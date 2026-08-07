import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope } from '../lib/scope'
import { specToTOML } from '../lib/toml'
import { Button } from './ui/button'
import { ErrorState, Note } from './shared'
import { DiffTable } from './deploy-dialog'
import { SecretForm } from './application-secrets'
import { FormPage, FormHint, FormSection } from './form-page'
import { ServiceIcon } from './service-icon'

export default function TemplateForm({
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
  const [architecture, setArchitecture] = useState('')
  const [siteURL, setSiteURL] = useState('')
  const [provider, setProvider] = useState('openai')
  const [providerURL, setProviderURL] = useState('')
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
            architecture,
            ...(template.site_url_required ? { site_url: siteURL } : {}),
            ...(template.id === 'open-webui'
              ? {
                  provider,
                  provider_url: provider === 'openai-compatible' ? providerURL : '',
                  model,
                }
              : {}),
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
  if (!template.deployable)
    return (
      <FormPage
        title={template.name}
        description={template.description}
        breadcrumbs={[{ label: 'Templates', to: '/templates' }, { label: template.name }]}
        icon="book"
      >
        <FormSection title="Deployment prerequisites" icon="book">
          <ServiceIcon name={template.id} size={42} />
          {template.requirements.map((requirement) => (
            <Note key={requirement}>{requirement}</Note>
          ))}
          <p>{template.verification}</p>
          {template.upstream.startsWith('https://') && (
            <a
              href={template.upstream}
              target="_blank"
              rel="noreferrer"
              className="button button-primary"
            >
              Read the upstream deployment guide
            </a>
          )}
        </FormSection>
      </FormPage>
    )
  return (
    <FormPage
      breadcrumbs={[{ label: 'Templates', to: '/templates' }, { label: template.name }]}
      icon="box"
      help={
        <>
          <div className="template-identity">
            <ServiceIcon name={template.id} size={42} />
            <h2>{template.name}</h2>
            <p>{template.description}</p>
            <small>{template.license}</small>
          </div>
          <FormHint title="Your own installation">
            This creates a regular Hakopod application with an immutable image and an explicit
            configuration.
          </FormHint>
          <FormHint title="Resources">{template.resource_summary}</FormHint>
          <FormHint title="Image verification">{template.verification}</FormHint>
          {template.requirements.map((item) => (
            <FormHint key={item} title="Requirement">
              {item}
            </FormHint>
          ))}
        </>
      }
      title={plan ? `Review ${plan.spec.name}` : `Configure ${template.name}`}
      description={`${scope.project} / ${scope.environment} · ${plan ? 'Review the exact revision before deploying.' : template.description}`}
    >
      <div className="form-body auth-form">
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
          <FormSection
            title="Application configuration"
            description="Choose a name and allocate persistent storage."
            icon="box"
          >
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
            <label>
              Target architecture
              <select
                value={architecture}
                onChange={(event) => setArchitecture(event.target.value)}
              >
                <option value="">Infer from a uniform cluster</option>
                {template.architectures.map((value) => (
                  <option key={value} value={value}>
                    Linux {value.toUpperCase()}
                  </option>
                ))}
              </select>
            </label>
            {template.site_url_required && (
              <label>
                Canonical site URL
                <input
                  type="url"
                  value={siteURL}
                  onChange={(event) => setSiteURL(event.target.value)}
                  required
                  maxLength={512}
                  placeholder="https://service.example.com"
                />
                <span className="field-help">
                  Use the HTTPS origin that people will use to open this application.
                </span>
              </label>
            )}
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
                    required
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
            {template.id === 'open-webui' && (
              <>
                <label>
                  Model provider
                  <select value={provider} onChange={(event) => setProvider(event.target.value)}>
                    {template.providers.map((value) => (
                      <option key={value} value={value}>
                        {value === 'openai'
                          ? 'OpenAI'
                          : value === 'openai-compatible'
                            ? 'OpenAI-compatible endpoint'
                            : value}
                      </option>
                    ))}
                  </select>
                </label>
                {provider === 'openai-compatible' && (
                  <label>
                    Provider API URL
                    <input
                      type="url"
                      value={providerURL}
                      onChange={(event) => setProviderURL(event.target.value)}
                      maxLength={512}
                      placeholder="https://provider.example.com/v1"
                      required
                    />
                  </label>
                )}
                <label>
                  Model
                  <input
                    value={model}
                    onChange={(event) => setModel(event.target.value)}
                    maxLength={200}
                    placeholder="Model identifier from your provider"
                    required
                  />
                </label>
                <Note>
                  Provider credentials are saved as write-only application secrets during review.
                </Note>
              </>
            )}
            {template.configuration === 'workspace' && (
              <Note>
                Choose models, providers, and credentials inside the application workspace after
                completing its administrator setup. Supported providers:{' '}
                {template.providers.join(', ')}.
              </Note>
            )}
          </FormSection>
        )}
        {error && (
          <div className="inline-error" role="alert">
            {error}
          </div>
        )}
      </div>
      <div className="form-footer">
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
            (template.site_url_required && !siteURL) ||
            (template.id === 'open-webui' && provider === 'openai-compatible' && !providerURL) ||
            (['vllm', 'open-webui'].includes(template.id) && !model.trim()) ||
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
    </FormPage>
  )
}
