import type { Application } from '../lib/types'
import { fieldError } from '../lib/form-errors'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { ErrorState, Note, RequestError } from './shared'
import { DiffTable } from './deploy-dialog'
import { TemplateSecretField } from './template-secret-field'
import { TOMLCode } from './toml-code'
import { FormPage, FormHint, FormSection } from './form-page'
import { ServiceIcon } from './service-icon'
import { ComputeNotice } from './compute-notice'
import { useEditionFeatures } from '../lib/dashboard-edition'
import { workloadRequirementLabel } from '../lib/template-requirements'

export default function TemplateForm({
  template,
  application,
  onClose,
}: {
  application?: Application
  template: components['schemas']['Template']
  onClose: () => void
}) {
  const scope = useScope()
  const features = useEditionFeatures()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const serviceNameInput = useRef<HTMLInputElement>(null)
  const [name, setName] = useState(application?.name || '')
  const [serviceName, setServiceName] = useState(
    application && !template.workload_requirements?.includes('multiple_services')
      ? template.id.replaceAll(/[^a-z0-9-]/g, '-').slice(0, 40)
      : '',
  )
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries((template.config_fields || []).map((field) => [field.name, field.default])),
  )
  const [isPublic, setPublic] = useState(false)
  const [storage, setStorage] = useState(
    features.hostedStorageGiB
      ? Math.min(5, features.hostedStorageGiB)
      : template.id === 'vllm'
        ? 30
        : 5,
  )
  const [model, setModel] = useState('')
  const [revision, setRevision] = useState('')
  const [architecture, setArchitecture] = useState(features.hostedCompute ? 'amd64' : '')
  const [siteURL, setSiteURL] = useState('')
  const [provider, setProvider] = useState('openai')
  const [providerURL, setProviderURL] = useState('')
  const [databaseName, setDatabaseName] = useState('app')
  const [databaseUser, setDatabaseUser] = useState('hakopod')
  const [useModelToken, setUseModelToken] = useState(false)
  const [secretDrafts, setSecretDrafts] = useState<Record<string, string>>({})
  const [plan, setPlan] = useState<components['schemas']['TemplatePlan'] | null>(null)
  const [key, setKey] = useState('')
  const [saveSecret, setSaveSecret] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const query = {
    project: plan?.configuration.project || scope.project,
    environment: plan?.configuration.environment || scope.environment,
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
    if (
      application &&
      !template.workload_requirements?.includes('multiple_services') &&
      !/^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$/.test(serviceName)
    ) {
      serviceNameInput.current?.focus()
      setError('service_name: Use a unique name with 1–40 lowercase letters, digits or hyphens.')
      return
    }
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
            ...(serviceName ? { service_name: serviceName } : {}),
            ...(application
              ? { application_id: application.id, expected_revision: application.revision }
              : {}),
            public: isPublic,
            values,
            storage_gib: storage,
            ...(features.hostedFree && ['postgresql', 'redis', 'mysql'].includes(template.id)
              ? { size: 'small' as const }
              : {}),
            architecture,
            ...(template.site_url_supported ? { site_url: siteURL } : {}),
            ...(template.database_config
              ? { database_name: databaseName, database_user: databaseUser }
              : {}),
            ...(template.id === 'open-webui'
              ? {
                  provider,
                  provider_url: provider === 'openai-compatible' ? providerURL : '',
                  model,
                }
              : {}),
            ...(template.id === 'vllm'
              ? { model, model_revision: revision, use_model_token: useModelToken }
              : {}),
          },
        }),
      )
      if (
        result.expected_revision !== (application?.revision || 0) ||
        (application && result.application_id !== application.id)
      )
        throw new Error(
          'The application changed or this name is already taken. Reload and review the template again.',
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
        client.POST('/templates/{id}/deploy', {
          params: { path: { id: template.id }, header: { 'Idempotency-Key': key } },
          body: {
            configuration: plan.configuration,
            toml: plan.toml,
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
  const unsupportedRequirements = (template.workload_requirements || []).filter((requirement) => {
    if (requirement === 'persistent_storage' && features.hostedStorageGiB > 0) return false
    if (
      ['larger_service', 'multiple_services', 'multiple_replicas'].includes(requirement) &&
      !features.hostedFree
    )
      return false
    if (
      requirement === 'larger_service' &&
      features.hostedFree &&
      ['postgresql', 'redis', 'mysql'].includes(template.id)
    )
      return false
    return true
  })
  if (template.deployable && features.hostedCompute && unsupportedRequirements.length)
    return (
      <FormPage title={template.name} description={template.description} breadcrumbs={[]}>
        <section className="grid gap-4 py-6">
          <h2>
            {unsupportedRequirements.length === 1 &&
            unsupportedRequirements[0] === 'persistent_storage'
              ? 'Persistent storage is being prepared'
              : 'Requires your own server'}
          </h2>
          <p>
            {unsupportedRequirements.length === 1 &&
            unsupportedRequirements[0] === 'persistent_storage'
              ? 'The Cloud operator has not enabled persistent storage yet. Your compute allocation is unchanged.'
              : `${template.name} needs capabilities outside your hosted compute allocation:`}
          </p>
          <ul className="list-disc pl-5">
            {unsupportedRequirements.map((requirement) => (
              <li key={requirement}>
                {workloadRequirementLabel[requirement] || requirement.replaceAll('_', ' ')}
              </li>
            ))}
          </ul>
          <p className="text-sm muted-text">
            {unsupportedRequirements.length === 1 &&
            unsupportedRequirements[0] === 'persistent_storage'
              ? 'Try again once storage is available, or connect your own server.'
              : 'Connect a server you own to use this template. Existing hosted resources must be released before changing compute.'}
          </p>
          <div className="flex flex-wrap gap-2">
            <Button variant="primary" asChild>
              <a href={features.computeURL}>Choose compute</a>
            </Button>
            <Button onClick={onClose}>Back to catalog</Button>
          </div>
        </section>
      </FormPage>
    )
  if (!template.deployable)
    return (
      <FormPage
        title={template.name}
        description={template.description}
        breadcrumbs={[{ label: 'Templates', to: '/templates' }, { label: template.name }]}
        icon="book"
      >
        <FormSection title="Deployment prerequisites" icon="book">
          <ServiceIcon
            background={template.logo_background}
            src={template.logo || undefined}
            name={template.id}
            size={42}
          />
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
        plan ? null : (
          <>
            <div className="template-identity">
              <ServiceIcon
                background={template.logo_background}
                src={template.logo || undefined}
                name={template.id}
                size={42}
              />
              <h2>{template.name}</h2>
              <p>{template.description}</p>
              <small>{template.license}</small>
            </div>
            <FormHint title="What this creates">
              {application
                ? 'Adds the template’s services to this application after review.'
                : 'Creates an application with an immutable image and an explicit configuration.'}
            </FormHint>
            <FormHint title="Resources">{template.resource_summary}</FormHint>
            <FormHint title="Image verification">{template.verification}</FormHint>
            {template.requirements.map((item) => (
              <FormHint key={item} title="Requirement">
                {item}
              </FormHint>
            ))}
            <FormHint title="Official setup references">
              {template.sources.map((source, index) => (
                <p key={source}>
                  <a href={source} target="_blank" rel="noreferrer">
                    {index ? 'Additional requirements' : 'Upstream setup guide'}
                  </a>
                </p>
              ))}
            </FormHint>
          </>
        )
      }
      title={plan ? `Review ${plan.spec.name}` : `Configure ${template.name}`}
      description={`${scope.project} / ${scope.environment} · ${plan ? 'Review the exact revision before deploying.' : template.description}`}
    >
      <div className="form-body auth-form">
        <ComputeNotice creatingApplication={!application} />
        {application && (
          <Note>
            Adding services to <strong>{application.display_name || application.name}</strong>.
            Existing services are retained. Service names and volume names must be unique.
          </Note>
        )}
        {plan ? (
          <>
            {!!plan.warnings.length && (
              <Note>
                <ul className="template-review-notes" aria-label="Deployment notes">
                  {plan.warnings.map((warning) => (
                    <li key={warning}>{warning}</li>
                  ))}
                </ul>
              </Note>
            )}
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
                {template.secret_fields
                  .filter((field) => plan.required_secrets.includes(field.name))
                  .map((field) => (
                    <div className="settings-list-row" key={field.name}>
                      <div>
                        <strong className="mono">{field.name}</strong>
                        <small>
                          {missing.includes(field.name)
                            ? 'Required before deployment'
                            : 'Saved; checked again when you deploy'}
                        </small>
                        <p className="field-help">{field.description}</p>
                      </div>
                      <Button
                        size="sm"
                        disabled={busy || (!!application && !missing.includes(field.name))}
                        onClick={() => setSaveSecret(field.name)}
                      >
                        {missing.includes(field.name)
                          ? 'Set value'
                          : application
                            ? 'Using saved secret'
                            : 'Replace'}
                      </Button>
                    </div>
                  ))}
                {saveSecret && (
                  <TemplateSecretField
                    key={saveSecret}
                    templateId={template.id}
                    field={template.secret_fields.find((field) => field.name === saveSecret)!}
                    query={query}
                    value={secretDrafts[saveSecret] || ''}
                    onChange={(value) =>
                      setSecretDrafts((drafts) => ({ ...drafts, [saveSecret]: value }))
                    }
                    replacing={!missing.includes(saveSecret)}
                    busy={busy}
                    onBusy={setBusy}
                    onCancel={() => setSaveSecret('')}
                    onSaved={() => {
                      setSecretDrafts((drafts) => ({ ...drafts, [saveSecret]: '' }))
                      setSaveSecret('')
                      void secrets.refetch()
                    }}
                  />
                )}
              </section>
            )}
            <DiffTable changes={plan.changes} />
            <details className="config-details">
              <summary>Canonical configuration</summary>
              <TOMLCode code={plan.toml} />
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
              <Input
                value={name}
                disabled={!!application}
                error={fieldError(error, 'name')}
                onChange={(e) => setName(e.target.value)}
                placeholder="Choose a name"
                pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?"
                maxLength={40}
                required
              />
            </label>
            {application && !template.workload_requirements?.includes('multiple_services') && (
              <label>
                Service name
                <Input
                  required
                  ref={serviceNameInput}
                  value={serviceName}
                  error={fieldError(error, 'service_name')}
                  onChange={(event) => setServiceName(event.target.value)}
                  pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?"
                  maxLength={40}
                />
                <span className="field-help">
                  Choose a unique name for this service inside the application.
                </span>
              </label>
            )}
            {template.workload_requirements?.includes('persistent_storage') && (
              <label>
                Persistent storage per service (GiB)
                <Input
                  type="number"
                  min={1}
                  max={features.hostedCompute ? features.hostedStorageGiB : 200}
                  value={storage}
                  onChange={(e) => setStorage(Number(e.target.value))}
                  error={fieldError(error, 'storage_gib')}
                />
              </label>
            )}
            <div className="grid gap-1">
              <span>Target architecture</span>
              <SelectField
                label="Target architecture"
                value={architecture}
                error={fieldError(error, 'architecture')}
                onValueChange={setArchitecture}
                options={[
                  { value: '', label: 'Infer from a uniform cluster' },
                  ...template.architectures.map((value) => ({
                    value,
                    label: `Linux ${value.toUpperCase()}${features.hostedCompute && value !== 'amd64' ? ' · Requires your own server' : ''}`,
                    disabled: features.hostedCompute && value !== 'amd64',
                  })),
                ]}
              />
            </div>
            {(template.config_fields || []).map((field) => (
              <div className="grid gap-1" key={field.name}>
                <label htmlFor={`template-config-${field.name}`}>{field.label}</label>
                <Input
                  id={`template-config-${field.name}`}
                  aria-describedby={`template-help-${field.name}`}
                  value={values[field.name] || ''}
                  error={fieldError(error, `values.${field.name}`)}
                  required={field.required}
                  maxLength={2048}
                  onChange={(event) =>
                    setValues((current) => ({ ...current, [field.name]: event.target.value }))
                  }
                />
                <span className="field-help" id={`template-help-${field.name}`}>
                  {field.description}
                </span>
              </div>
            ))}
            {template.database_config && (
              <>
                <label>
                  Database name
                  <Input
                    value={databaseName}
                    error={fieldError(error, 'database_name')}
                    onChange={(event) => setDatabaseName(event.target.value)}
                    pattern="[a-z][a-z0-9_]*"
                    maxLength={32}
                    required
                  />
                </label>
                <label>
                  Database user
                  <Input
                    value={databaseUser}
                    error={fieldError(error, 'database_user')}
                    onChange={(event) => setDatabaseUser(event.target.value)}
                    pattern="[a-z][a-z0-9_]*"
                    maxLength={32}
                    required
                  />
                </label>
                <p className="field-help">
                  These values initialize a new volume. Existing database users and passwords
                  require a database migration. PostgreSQL creates this initial user with
                  administrator rights.
                </p>
              </>
            )}
            {template.site_url_supported && (
              <label>
                Canonical site URL{template.site_url_required ? '' : ' (optional)'}
                <Input
                  type="url"
                  value={siteURL}
                  error={fieldError(error, 'site_url')}
                  onChange={(event) => setSiteURL(event.target.value)}
                  required={template.site_url_required}
                  maxLength={512}
                  placeholder="https://service.example.com"
                />
                <span className="field-help">
                  Use the HTTPS origin that people will use to open this application.
                </span>
              </label>
            )}
            {template.category !== 'database' && (
              <label className="checkbox-row">
                <Input
                  type="checkbox"
                  checked={isPublic}
                  onChange={(e) => setPublic(e.target.checked)}
                />
                Expose supported HTTP service publicly
              </label>
            )}
            {template.category === 'database' && (
              <p className="field-help">
                This database uses a private service endpoint. Public exposure is disabled.
              </p>
            )}
            {template.id === 'vllm' && (
              <>
                <label>
                  Hugging Face model
                  <Input
                    value={model}
                    error={fieldError(error, 'model')}
                    onChange={(e) => setModel(e.target.value)}
                    placeholder="organization/model"
                    maxLength={200}
                    required
                  />
                </label>
                <label>
                  Model revision{useModelToken ? '' : ' (optional)'}
                  <Input
                    value={revision}
                    error={fieldError(error, 'model_revision')}
                    onChange={(e) => setRevision(e.target.value)}
                    placeholder="Resolve current immutable revision"
                    maxLength={64}
                  />
                </label>
                <label className="checkbox-row">
                  <Input
                    type="checkbox"
                    checked={useModelToken}
                    onChange={(event) => setUseModelToken(event.target.checked)}
                  />
                  Use a Hugging Face token for a private or gated model
                </label>
                {useModelToken && (
                  <p className="field-help">
                    Provide the exact 40-character model revision. Save a read token with approved
                    model access during review.
                  </p>
                )}
                <Note>
                  Requires an NVIDIA GPU, compatible device plugin, storage, and enough GPU memory
                  for the selected model. Remote model code is disabled.
                </Note>
              </>
            )}
            {template.id === 'open-webui' && (
              <>
                <SelectField
                  label="Model provider"
                  value={provider}
                  onValueChange={setProvider}
                  options={template.providers.map((value) => ({
                    value,
                    label:
                      value === 'openai'
                        ? 'OpenAI'
                        : value === 'openai-compatible'
                          ? 'OpenAI-compatible endpoint'
                          : value,
                  }))}
                />
                {provider === 'openai-compatible' && (
                  <label>
                    Provider API URL
                    <Input
                      type="url"
                      value={providerURL}
                      error={fieldError(error, 'provider_url')}
                      onChange={(event) => setProviderURL(event.target.value)}
                      maxLength={512}
                      placeholder="https://provider.example.com/v1"
                      required
                    />
                  </label>
                )}
                <label>
                  Model
                  <Input
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
        {error && <RequestError error={error} />}
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
            (template.config_fields || []).some(
              (field) => field.required && !values[field.name]?.trim(),
            ) ||
            (template.workload_requirements?.includes('persistent_storage') &&
              (!Number.isInteger(storage) ||
                storage < 1 ||
                storage > (features.hostedCompute ? features.hostedStorageGiB : 200))) ||
            (template.database_config && (!databaseName || !databaseUser)) ||
            (template.id === 'vllm' && useModelToken && !/^[a-f0-9]{40}$/.test(revision)) ||
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
