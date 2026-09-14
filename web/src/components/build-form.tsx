import { GitConnectionField } from './git-connection-field'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { FormPage, FormHint, FormSection } from './form-page'
import { Note } from './shared'

type Build = components['schemas']['BuildConfig']
export default function BuildForm({
  build,
  application,
  onClose,
}: {
  build?: Build
  application?: Application
  onClose: () => void
}) {
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const project = build?.project || application?.project || scope.project
  const environment = build?.environment || application?.environment || scope.environment
  const [name, setName] = useState(build?.name || application?.name || '')
  const [service, setService] = useState(
    build?.service || Object.keys(application?.spec.services || {})[0] || 'web',
  )
  const [provider, setProvider] = useState<'github' | 'gitlab'>(build?.provider || 'github')
  const [connectionId, setConnectionId] = useState(build?.connection_id || '')
  const [repository, setRepository] = useState(build?.repository || '')
  const [branch, setBranch] = useState(build?.branch || 'main')
  const [mode, setMode] = useState<Build['mode']>(build?.mode || 'dockerfile')
  const [preset, setPreset] = useState<Build['preset']>(build?.preset || 'auto')
  const [architecture, setArchitecture] = useState<Build['architecture'] | ''>(
    build?.architecture || '',
  )
  const [context, setContext] = useState(build?.context_path || '.')
  const [dockerfile, setDockerfile] = useState(build?.dockerfile || 'Dockerfile')
  const [port, setPort] = useState(build?.port || 8080)
  const [isPublic, setPublic] = useState(build?.public || false)
  const [size, setSize] = useState(build?.size || 'small')
  const [registry, setRegistry] = useState(build?.registry_credential || '')
  const [automatic, setAutomatic] = useState(build?.auto_build || false)
  const [autoDeploy, setAutoDeploy] = useState(build?.auto_deploy || false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const registries = useQuery({
    queryKey: ['registries', project, environment],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/registries', { signal, params: { query: { project, environment } } })),
    gcTime: 0,
  })
  const linked = Boolean(build?.application_id || application)
  return (
    <FormPage
      breadcrumbs={[
        { label: 'Source builds', to: '/builds' },
        ...(build ? [{ label: build.name, to: `/builds/${build.id}` }] : []),
        { label: build ? 'Edit build' : 'New source build' },
      ]}
      icon="branch"
      help={
        <>
          <FormHint title="Keep builds in Git">
            The reviewed workflow runs in your provider account. Hakopod deploys its verified image
            digest.
          </FormHint>
          <FormHint title="Private images">
            Save a registry credential for the selected provider before deploying images that
            require authentication.
          </FormHint>
        </>
      }
      title={build ? 'Edit source build' : 'Build an application from source'}
      description={`${project} / ${environment} · Build with a Dockerfile or Cloud Native Buildpacks.`}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          if (busy) return
          setBusy(true)
          setError('')
          try {
            const body = {
              project,
              environment,
              name,
              service,
              provider,
              connection_id: connectionId,
              repository,
              branch,
              mode,
              preset,
              context_path: context,
              architecture: architecture || undefined,
              dockerfile,
              port,
              public: isPublic,
              size,
              registry_credential: registry,
              auto_build: automatic,
              auto_deploy: autoDeploy,
              application_id: build?.application_id || application?.id || '',
              ...(build ? { expected_config_revision: build.revision } : {}),
            }
            const result = build
              ? await unwrap(
                  client.PUT('/builds/{id}', { params: { path: { id: build.id } }, body }),
                )
              : await unwrap(client.POST('/builds', { body }))
            void cache.invalidateQueries({ queryKey: ['builds'] })
            void cache.invalidateQueries({ queryKey: ['build', result.id] })
            onClose()
            void navigate({ to: '/builds/$buildId', params: { buildId: result.id } })
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="form-body auth-form">
          <FormSection
            title="Repository"
            description="Select the application, provider, and source branch."
            icon="branch"
          >
            <div className="form-grid">
              <label>
                Application name
                <Input
                  value={name}
                  readOnly={Boolean(build || application)}
                  onChange={(e) => setName(e.target.value)}
                  pattern="[a-z][a-z0-9-]*"
                  maxLength={63}
                  required
                />
              </label>
              <label>
                Service
                {application ? (
                  <SelectField
                    label="Service"
                    value={service}
                    onValueChange={(value) => setService(value)}
                    options={
                      Object.keys(application.spec.services).map((item) => ({
                        value: item,
                        label: item,
                      })) ?? []
                    }
                  />
                ) : (
                  <Input
                    value={service}
                    readOnly={Boolean(build)}
                    onChange={(e) => setService(e.target.value)}
                    pattern="[a-z][a-z0-9-]*"
                    maxLength={63}
                    required
                  />
                )}
              </label>
            </div>
            <label>
              Git provider
              <SelectField
                label="Git provider"
                value={provider}
                onValueChange={(value) => {
                  setProvider(value as 'github' | 'gitlab')
                  setConnectionId('')
                }}
                options={[
                  {
                    value: 'github',
                    label: 'GitHub Actions + GHCR',
                  },
                  {
                    value: 'gitlab',
                    label: 'GitLab CI + GitLab Container Registry',
                  },
                ]}
              />
            </label>
            <GitConnectionField
              provider={provider}
              value={connectionId}
              onValueChange={setConnectionId}
              builds
            />
            <div className="form-grid">
              <label>
                Repository
                <Input
                  value={repository}
                  onChange={(e) => setRepository(e.target.value)}
                  placeholder="owner/repository"
                  maxLength={201}
                  required
                />
              </label>
              <label>
                Source branch
                <Input
                  value={branch}
                  onChange={(e) => setBranch(e.target.value)}
                  maxLength={200}
                  required
                />
              </label>
            </div>
          </FormSection>
          <FormSection
            title="Build recipe"
            description="Paths are relative to the repository root."
            icon="code"
          >
            <div className="form-grid">
              <label>
                Build method
                <SelectField
                  label="Build method"
                  value={mode}
                  onValueChange={(value) => setMode(value as Build['mode'])}
                  options={[
                    {
                      value: 'dockerfile',
                      label: 'Dockerfile',
                    },
                    {
                      value: 'buildpacks',
                      label: 'Cloud Native Buildpacks',
                    },
                  ]}
                />
              </label>
              <label>
                Build context
                <Input
                  value={context}
                  onChange={(e) => setContext(e.target.value)}
                  maxLength={200}
                  required
                />
              </label>
            </div>
            <label>
              Target architecture
              <SelectField
                label="Target architecture"
                value={architecture}
                onValueChange={(value) => setArchitecture(value as Build['architecture'] | '')}
                options={[
                  {
                    value: '',
                    label: 'Infer from a uniform cluster',
                  },
                  {
                    value: 'amd64',
                    label: 'Linux AMD64',
                  },
                  {
                    value: 'arm64',
                    label: 'Linux ARM64',
                  },
                ]}
              />
            </label>
            {mode === 'dockerfile' ? (
              <label>
                Dockerfile path
                <Input
                  value={dockerfile}
                  onChange={(e) => setDockerfile(e.target.value)}
                  maxLength={200}
                  required
                />
              </label>
            ) : (
              <label>
                Buildpack preset
                <SelectField
                  label="Buildpack preset"
                  value={preset}
                  onValueChange={(value) => setPreset(value as Build['preset'])}
                  options={
                    ['auto', 'nodejs', 'python', 'go', 'java', 'dotnet', 'ruby', 'static'].map(
                      (value) => ({
                        value: value,
                        label: value,
                      }),
                    ) ?? []
                  }
                />
              </label>
            )}
          </FormSection>
          <FormSection
            title="Runtime"
            description="Choose resource and image-pull settings."
            icon="box"
          >
            {!linked && (
              <div className="form-grid">
                <label>
                  Service port
                  <Input
                    type="number"
                    min={1}
                    max={65535}
                    value={port}
                    onChange={(e) => setPort(Number(e.target.value))}
                    required
                  />
                </label>
                <label>
                  Resource profile
                  <SelectField
                    label="Resource profile"
                    value={size}
                    onValueChange={(value) => setSize(value)}
                    options={
                      ['small', 'medium', 'large'].map((value) => ({
                        value: value,
                        label: value,
                      })) ?? []
                    }
                  />
                </label>
              </div>
            )}
            {!linked && (
              <label className="checkbox-row">
                <Input
                  type="checkbox"
                  checked={isPublic}
                  onChange={(e) => setPublic(e.target.checked)}
                />
                Expose the service publicly
              </label>
            )}
            <label>
              Runtime registry credential
              <SelectField
                label="Runtime registry credential"
                value={registry}
                onValueChange={(value) => setRegistry(value)}
                options={[
                  {
                    value: '',
                    label: 'None · image must be publicly pullable',
                  },
                  ...(registry && !registries.data?.items.some((item) => item.name === registry)
                    ? [{ value: registry, label: registry }]
                    : []),
                  ...(registries.data?.items.map((item) => ({
                    value: item.name,
                    label: item.name + ' · ' + item.registry,
                  })) ?? []),
                ]}
              />
              <span className="field-help">
                Private registry images need a saved credential with package read permission.
              </span>
            </label>
          </FormSection>
          <FormSection
            title="Automation"
            description="Choose how verified commits become deployments."
            icon="refresh"
          >
            <label className="checkbox-row">
              <Input
                type="checkbox"
                checked={automatic}
                onChange={(e) => {
                  setAutomatic(e.target.checked)
                  if (!e.target.checked) setAutoDeploy(false)
                }}
              />
              Build automatically on pushes to this source branch
            </label>
            <label className="checkbox-row">
              <Input
                type="checkbox"
                checked={autoDeploy}
                disabled={!automatic}
                onChange={(e) => setAutoDeploy(e.target.checked)}
              />
              Deploy successful verified builds automatically
            </label>
            <Note>
              {linked
                ? 'A successful build replaces the selected service image. Existing service resources and networking remain controlled by its application configuration.'
                : 'The application is created when a verified build image is deployed. No container image is needed now.'}{' '}
              Saving build settings prepares a workflow preview. An administrator explicitly
              installs the reviewed workflow in {provider === 'gitlab' ? 'GitLab' : 'GitHub'}.
            </Note>
            {provider === 'gitlab' && (
              <Note>
                Hakopod manages one .gitlab-ci.yml entrypoint per repository. Installation refuses
                an existing unowned CI file or a file owned by another build. Configure Pipeline
                events on the project webhook for automatic deployment.
              </Note>
            )}
            {automatic && (
              <Note>
                Automatic builds require the installed workflow. Automatic deployment additionally
                needs the{' '}
                {provider === 'gitlab'
                  ? 'authenticated GitLab Pipeline Hook'
                  : 'signed GitHub workflow event'}{' '}
                integration and your continuing project authority.
              </Note>
            )}
          </FormSection>
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="form-footer">
          <Button type="button" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : build ? 'Save build configuration' : 'Create source build'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
