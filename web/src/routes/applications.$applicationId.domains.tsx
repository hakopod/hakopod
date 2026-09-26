import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useScope, useResourceScope } from '../lib/scope'
import type { components } from '../lib/api.generated'
import type { Plan } from '../lib/types'
import { FormPage, FormSection, FormHint } from '../components/form-page'
import { DiffTable } from '../components/deploy-dialog'
import { Button } from '../components/ui/button'
import { Copy, Empty, ErrorState, Loading, Note, Status } from '../components/shared'
// The provider listing for an application deliberately carries no credential and
// no zone filter, only the id, name and kind a chooser needs.
type DNSProviderChoice = components['schemas']['DNSProviderSummary'] & { id: string }
type RecordAction = { hostnames: string[]; truncated: boolean; replace: boolean }
type RecordResult = components['schemas']['DNSRecordResult']
const recordStatusCopy: Record<string, string> = {
  created: 'Created at the provider',
  exists: 'Already present, left as it is',
  conflict: 'Something else is already at that name',
  failed: 'Not created',
  skipped: 'Skipped',
}
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
  const dnsProviders = useQuery({
    queryKey: ['application-dns-providers', applicationId],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/domains/dns-providers', {
          signal,
          params: { path: { id: applicationId } },
        }),
      ),
    enabled: scope.can('deployments:write'),
    gcTime: 0,
  })
  useResourceScope(application.data)
  const [hostname, setHostname] = useState('')
  const [service, setService] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [plan, setPlan] = useState<Plan | null>(null)
  const [key, setKey] = useState('')
  const [domainAction, setDomainAction] = useState('')
  const [recordAction, setRecordAction] = useState<RecordAction | null>(null)
  const [recordResults, setRecordResults] = useState<RecordResult[] | null>(null)
  const [recordProvider, setRecordProvider] = useState('')
  if (application.isPending || domains.isPending) return <Loading />
  if (application.error || domains.error || !application.data)
    return <ErrorState error={application.error || domains.error} />
  const app = application.data
  const publicServices = Object.entries(app.spec.services)
    .filter(([, value]) => value.public && value.port)
    .map(([name]) => name)
  const selected = service || publicServices[0] || ''
  const canWrite = scope.can('deployments:write')
  const providers: DNSProviderChoice[] = canWrite
    ? (dnsProviders.data?.items || []).filter((item): item is DNSProviderChoice => Boolean(item.id))
    : []
  const provider = providers.find((item) => item.id === recordProvider) || providers[0]
  const awaitingDNS = (domains.data?.items || []).filter((domain) => !domain.verified)
  const conflicting = (recordResults || [])
    .filter((result) => result.status === 'conflict')
    .map((result) => result.hostname)
  function reviewRecords(hostnames: string[], replace = false) {
    setError('')
    setRecordResults(replace ? recordResults : null)
    setRecordProvider(provider?.id || '')
    setRecordAction({
      hostnames: hostnames.slice(0, 20),
      truncated: hostnames.length > 20,
      replace,
    })
    setKey(crypto.randomUUID())
  }
  async function createRecords() {
    if (!recordAction || !recordProvider || busy) return
    setBusy(true)
    setError('')
    try {
      const result = await unwrap(
        client.POST('/applications/{id}/domains/dns-records', {
          params: { path: { id: applicationId }, header: { 'Idempotency-Key': key } },
          body: {
            provider_id: recordProvider,
            hostnames: recordAction.hostnames,
            replace_existing: recordAction.replace,
          },
        }),
      )
      setRecordResults(result.results)
      setRecordAction(null)
      void cache.invalidateQueries({ queryKey: ['application', applicationId] })
      await domains.refetch()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
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
      setDomainAction(
        target
          ? `Activate ${domain} for ${target} after DNS ownership verification. This revision activates all verified mappings in its configuration; unverified domains stay inactive.`
          : `Remove ${domain} from this application’s domain configuration.`,
      )
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
      title={
        plan ? 'Review domain changes' : recordAction ? 'Review DNS records' : `${app.name} domains`
      }
      description="Prove domain ownership, review the application revision, then activate routing."
      breadcrumbs={[
        { label: 'Applications', to: `/projects/${encodeURIComponent(app.project)}` },
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
            <Note>{domainAction}</Note>
            <dl className="grid grid-cols-[minmax(0,1fr)_auto] gap-2">
              {Object.entries(plan.spec.domains || {}).map(([host, service]) => (
                <div key={host} className="contents">
                  <dt className="break-all">{host}</dt>
                  <dd>{service}</dd>
                </div>
              ))}
            </dl>
            <DiffTable changes={plan.changes} />
            {plan.warnings.map((warning) => (
              <Note key={warning}>{warning}</Note>
            ))}
          </FormSection>
        ) : recordAction ? (
          <FormSection
            title="Review DNS records"
            description="The records that will be created at your DNS provider."
          >
            <Note>
              Creating records is not verifying them. A provider accepting a record means it reached
              that provider, not that it resolves yet, so these domains stay awaiting DNS until you
              verify them.
            </Note>
            <label>
              DNS provider
              <SelectField
                label="DNS provider"
                value={recordProvider}
                onValueChange={(value) => setRecordProvider(value)}
                required
                options={providers.map((item) => ({ value: item.id, label: item.name }))}
              />
            </label>
            {recordAction.truncated && (
              <Note>
                At most 20 hostnames are written at a time. Run this again for the rest once these
                are created.
              </Note>
            )}
            {recordAction.hostnames.map((host) => {
              const domain = domains.data?.items.find((item) => item.hostname === host)
              const existing = (recordResults || []).find((result) => result.hostname === host)
              return (
                <div key={host} className="domain-record">
                  <h3 className="break-all">{host}</h3>
                  <dl className="service-definition-list">
                    <div>
                      <dt>TXT {domain?.verification_name}</dt>
                      <dd className="break-text">
                        <code>{domain?.verification_value}</code>
                      </dd>
                    </div>
                    {Boolean(domain?.target) && (
                      <div>
                        <dt>CNAME {host}</dt>
                        <dd className="break-text">
                          <code>{domain?.target}</code>
                        </dd>
                      </div>
                    )}
                    {recordAction.replace && existing && (
                      <div>
                        <dt>Reported at that name</dt>
                        <dd className="break-text">{existing.message}</dd>
                      </div>
                    )}
                  </dl>
                </div>
              )
            })}
            {conflicting.some((host) => recordAction.hostnames.includes(host)) ? (
              <label className="checkbox-row">
                <Input
                  type="checkbox"
                  checked={recordAction.replace}
                  onChange={(event) =>
                    setRecordAction({ ...recordAction, replace: event.target.checked })
                  }
                />
                Replace what is already at these names
              </label>
            ) : (
              <p className="field-help">
                Records that already exist are reported, never replaced. Replacing is offered once a
                name is reported as taken by something else.
              </p>
            )}
            {recordAction.replace && (
              <Note>
                Replacing overwrites the record that occupies each name above. hakopod does not read
                that value back, so check it at your DNS provider first: anything else pointing at
                those names stops working.
              </Note>
            )}
          </FormSection>
        ) : (
          <>
            {recordResults && (
              <FormSection
                title="DNS record results"
                description="What your DNS provider reported for each hostname."
              >
                <Note>
                  Records that were created have reached your DNS provider and have not propagated
                  yet. These domains stay awaiting DNS; use Verify DNS once the records resolve.
                </Note>
                {recordResults.map((result) => (
                  <div key={result.hostname} className="domain-record">
                    <div className="section-toolbar">
                      <div>
                        <h3 className="break-all">{result.hostname}</h3>
                        <p>{recordStatusCopy[result.status] || result.status}</p>
                      </div>
                    </div>
                    {result.message && <p className="break-text m-0 text-sm">{result.message}</p>}
                    {Boolean(result.records?.length) && (
                      <>
                        <p className="field-help">
                          {result.status === 'created'
                            ? 'These records are now at the provider.'
                            : 'These are the records for this hostname; they were not written now.'}
                        </p>
                        <dl className="service-definition-list">
                          {result.records?.map((record) => (
                            <div key={`${record.type} ${record.name}`}>
                              <dt>
                                {record.type} {record.name}
                              </dt>
                              <dd className="break-text">
                                <code>{record.value}</code>
                              </dd>
                            </div>
                          ))}
                        </dl>
                      </>
                    )}
                  </div>
                ))}
                <div className="toolbar-actions">
                  {canWrite && conflicting.length > 0 && (
                    <Button disabled={busy} onClick={() => reviewRecords(conflicting, true)}>
                      Review replacing {conflicting.length} taken name
                      {conflicting.length === 1 ? '' : 's'}
                    </Button>
                  )}
                  <Button disabled={busy} onClick={() => setRecordResults(null)}>
                    Dismiss results
                  </Button>
                </div>
              </FormSection>
            )}
            <Note>
              Configured means ownership was verified and the mapping was accepted for routing.
              Deployment status, DNS resolution, and certificate readiness are reported separately.
            </Note>
            <FormSection
              title="Custom domains"
              description="Saved domain mappings and ownership verification for this application."
              icon="globe"
            >
              {canWrite && providers.length > 0 && awaitingDNS.length > 1 && (
                <div className="toolbar-actions">
                  <Button
                    disabled={busy}
                    onClick={() => reviewRecords(awaitingDNS.map((domain) => domain.hostname))}
                  >
                    Create DNS records for {awaitingDNS.length} domain
                    {awaitingDNS.length === 1 ? '' : 's'} awaiting DNS
                  </Button>
                </div>
              )}
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
                        {providers.length > 0 && (
                          <Button disabled={busy} onClick={() => reviewRecords([domain.hostname])}>
                            Create DNS records
                          </Button>
                        )}
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
                        {!app.spec.domains?.[domain.hostname] && (
                          <Button
                            disabled={busy}
                            onClick={async () => {
                              setBusy(true)
                              setError('')
                              try {
                                await unwrap(
                                  client.DELETE('/applications/{id}/domains/{hostname}', {
                                    params: {
                                      path: { id: applicationId, hostname: domain.hostname },
                                    },
                                    body: { expected_revision: app.revision },
                                  }),
                                )
                                void domains.refetch()
                              } catch (e) {
                                setError(message(e))
                              } finally {
                                setBusy(false)
                              }
                            }}
                          >
                            Discard setup record
                          </Button>
                        )}
                        {app.spec.domains?.[domain.hostname] && (
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
                    <SelectField
                      label="Public service"
                      value={selected}
                      onValueChange={(value) => setService(value)}
                      required
                      options={
                        publicServices.map((name) => ({
                          value: name,
                          label: name,
                        })) ?? []
                      }
                    />
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
        {recordAction ? (
          <>
            <Button disabled={busy} onClick={() => setRecordAction(null)}>
              Back to domains
            </Button>
            <Button
              variant="primary"
              disabled={busy || !canWrite || !recordProvider}
              onClick={() => void createRecords()}
            >
              {busy
                ? 'Creating records…'
                : recordAction.replace
                  ? 'Replace and create DNS records'
                  : 'Create DNS records'}
            </Button>
          </>
        ) : plan ? (
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
        ) : null}
      </div>
    </FormPage>
  )
}
