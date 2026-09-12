import { Input } from '../components/ui/input'
import { Select } from '../components/ui/select'
import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope, useResourceScope } from '../lib/scope'
import type { Plan } from '../lib/types'
import { FormPage, FormSection, FormHint } from '../components/form-page'
import { DiffTable } from '../components/deploy-dialog'
import { Button } from '../components/ui/button'
import { Copy, Empty, ErrorState, Loading, Note, Status } from '../components/shared'
export const Route = createFileRoute('/applications/$applicationId/domains')({
  component: ApplicationDomains,
})
function ApplicationDomains() {
  const { applicationId } = Route.useParams()
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const application = useQuery({
    queryKey: ['application', applicationId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: applicationId } } })),
    gcTime: 0,
  })
  const domains = useQuery({
    queryKey: ['application-domains', applicationId],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/domains', {
          signal,
          params: { path: { id: applicationId } },
        }),
      ),
    gcTime: 0,
  })
  useResourceScope(application.data)
  const [hostname, setHostname] = useState('')
  const [service, setService] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [plan, setPlan] = useState<Plan | null>(null)
  const [key, setKey] = useState('')
  if (application.isPending || domains.isPending) return <Loading />
  if (application.error || domains.error || !application.data)
    return <ErrorState error={application.error || domains.error} />
  const app = application.data
  const publicServices = Object.entries(app.spec.services)
    .filter(([, value]) => value.public && value.port)
    .map(([name]) => name)
  const selected = service || publicServices[0] || ''
  const canWrite = scope.can('deployments:write')
  async function review(domain: string, target?: string) {
    setBusy(true)
    setError('')
    try {
      const current = await unwrap(
        client.GET('/applications/{id}', { params: { path: { id: applicationId } } }),
      )
      const spec = structuredClone(current.spec)
      spec.domains = { ...spec.domains }
      if (target) spec.domains[domain] = target
      else delete spec.domains[domain]
      const result = await unwrap(
        client.POST('/plan', {
          body: { project: current.project, environment: current.environment, spec },
        }),
      )
      if (result.expected_revision !== current.revision)
        throw new Error('The application changed during review. Refresh and try again.')
      setPlan(result)
      setKey(crypto.randomUUID())
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <FormPage
      title={plan ? 'Review domain changes' : `${app.name} domains`}
      description="Prove domain ownership, review the application revision, then activate routing."
      breadcrumbs={[
        { label: 'Applications', to: '/' },
        { label: app.name, to: `/applications/${applicationId}` },
        { label: 'Custom domains' },
      ]}
      icon="globe"
      help={
        <>
          <FormHint title="Two DNS records">
            Use TXT to prove ownership and CNAME to send traffic to the generated service hostname.
            Verification alone does not activate routing.
          </FormHint>
          <FormHint title="Apex domains">
            For a root domain, use your ingress A/AAAA addresses or an alias supported by your DNS
            provider.
          </FormHint>
          <FormHint title="TLS coverage">
            Uploaded certificates must cover the generated service hostname and every configured
            custom name. The default issuer can manage these names.
          </FormHint>
        </>
      }
    >
      <div className="form-body">
        {plan ? (
          <FormSection
            title="Routing revision"
            description={`Revision ${plan.expected_revision} to ${plan.expected_revision + 1}`}
            icon="globe"
          >
            <DiffTable changes={plan.changes} />
            {plan.warnings.map((warning) => (
              <Note key={warning}>{warning}</Note>
            ))}
          </FormSection>
        ) : (
          <>
            <Note>
              Configured means the mapping is saved in the application. Deployment status, DNS
              resolution, and certificate readiness are reported separately.
            </Note>
            <FormSection
              title="Custom domains"
              description="Saved domain mappings and ownership verification for this application."
              icon="globe"
            >
              {!domains.data?.items.length ? (
                <Empty
                  icon="globe"
                  title="No custom domains"
                  description="Add a hostname to begin DNS ownership verification."
                />
              ) : (
                domains.data.items.map((domain) => (
                  <section className="domain-record" key={domain.hostname}>
                    <div className="section-toolbar">
                      <div>
                        <h3>{domain.hostname}</h3>
                        <p>{domain.service}</p>
                      </div>
                      <Status
                        value={
                          domain.active
                            ? 'configured'
                            : domain.verified
                              ? 'verified'
                              : 'awaiting DNS'
                        }
                        small
                      />
                    </div>
                    <dl className="service-definition-list">
                      <div>
                        <dt>TXT name</dt>
                        <dd>
                          <code>{domain.verification_name}</code>
                          <Copy value={domain.verification_name} />
                        </dd>
                      </div>
                      <div>
                        <dt>TXT value</dt>
                        <dd className="break-text">
                          <code>{domain.verification_value}</code>
                          <Copy value={domain.verification_value} />
                        </dd>
                      </div>
                      <div>
                        <dt>CNAME target</dt>
                        <dd>
                          <code>{domain.target}</code>
                          <Copy value={domain.target} />
                        </dd>
                      </div>
                    </dl>
                    {canWrite && (
                      <div className="toolbar-actions">
                        {!domain.verified && (
                          <Button
                            disabled={busy}
                            onClick={async () => {
                              setBusy(true)
                              setError('')
                              try {
                                await unwrap(
                                  client.POST('/applications/{id}/domains/{hostname}/verify', {
                                    params: {
                                      path: { id: applicationId, hostname: domain.hostname },
                                    },
                                    body: {},
                                  }),
                                )
                                void domains.refetch()
                              } catch (err) {
                                setError(message(err))
                              } finally {
                                setBusy(false)
                              }
                            }}
                          >
                            Verify DNS
                          </Button>
                        )}
                        {domain.verified && !domain.active && (
                          <Button
                            variant="primary"
                            disabled={busy}
                            onClick={() => void review(domain.hostname, domain.service)}
                          >
                            Review activation
                          </Button>
                        )}
                        {domain.active && (
                          <Button
                            variant="danger"
                            disabled={busy}
                            onClick={() => void review(domain.hostname)}
                          >
                            Review removal
                          </Button>
                        )}
                      </div>
                    )}
                  </section>
                ))
              )}
            </FormSection>
            {canWrite && (
              <FormSection
                title="Add a domain"
                description="Choose the public service that will receive traffic for this hostname."
                icon="plus"
              >
                <form
                  className="auth-form"
                  onSubmit={async (event) => {
                    event.preventDefault()
                    if (busy || !selected) return
                    setBusy(true)
                    setError('')
                    try {
                      await unwrap(
                        client.POST('/applications/{id}/domains', {
                          params: { path: { id: applicationId } },
                          body: { hostname, service: selected },
                        }),
                      )
                      setHostname('')
                      void domains.refetch()
                    } catch (err) {
                      setError(message(err))
                    } finally {
                      setBusy(false)
                    }
                  }}
                >
                  <label>
                    Hostname
                    <Input
                      value={hostname}
                      onChange={(event) => setHostname(event.target.value)}
                      placeholder="app.example.com"
                      maxLength={253}
                      required
                      autoCapitalize="none"
                      autoCorrect="off"
                    />
                  </label>
                  <label>
                    Public service
                    <Select
                      value={selected}
                      onChange={(event) => setService(event.target.value)}
                      required
                    >
                      {publicServices.map((name) => (
                        <option key={name}>{name}</option>
                      ))}
                    </Select>
                  </label>
                  {!publicServices.length && (
                    <Note>Configure a public HTTP service before adding a domain.</Note>
                  )}
                  <Button type="submit" variant="primary" disabled={busy || !selected}>
                    {busy ? 'Creating proof…' : 'Create DNS verification'}
                  </Button>
                </form>
              </FormSection>
            )}
          </>
        )}
        {error && <ErrorState error={error} />}
      </div>
      <div className="form-footer">
        {plan ? (
          <>
            <Button disabled={busy} onClick={() => setPlan(null)}>
              Back to domains
            </Button>
            <Button
              variant="primary"
              disabled={busy || !canWrite}
              onClick={async () => {
                setBusy(true)
                setError('')
                try {
                  const result = await unwrap(
                    client.POST('/deployments', {
                      params: { header: { 'Idempotency-Key': key } },
                      body: {
                        project: app.project,
                        environment: app.environment,
                        spec: plan.spec,
                        expected_revision: plan.expected_revision,
                      },
                    }),
                  )
                  void cache.invalidateQueries({ queryKey: ['application-domains', applicationId] })
                  void cache.invalidateQueries({ queryKey: ['application', applicationId] })
                  void navigate({
                    to: '/deployments/$deploymentId',
                    params: { deploymentId: result.id },
                  })
                } catch (err) {
                  setError(message(err))
                } finally {
                  setBusy(false)
                }
              }}
            >
              {busy ? 'Submitting…' : 'Apply reviewed domain changes'}
            </Button>
          </>
        ) : (
          <Link
            className="button"
            to="/applications/$applicationId"
            params={{ applicationId }}
            search={{ tab: 'networking' }}
          >
            Back to application
          </Link>
        )}
      </div>
    </FormPage>
  )
}
