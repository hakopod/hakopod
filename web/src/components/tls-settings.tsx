import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { useEffect, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { canAccess, useScope } from '../lib/scope'
import { dashboardEdition } from '../lib/dashboard-edition'
import { useAuthStatus, useInstallationAccess } from '../lib/installation-settings'
import type { components } from '../lib/api.generated'
import {
  findTLSIssuer,
  preferredTLSIssuer,
  preferredTLSMethod,
  tlsIssuerKey,
  tlsIssuerScope,
  tlsIssuersQueryKey,
  type TLSIssuer,
  type TLSIssuers,
  type TLSMethod,
} from '../lib/tls-issuers'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { HeadingHelp, Empty, ErrorState, Loading, Note, Status, RequestError } from './shared'

function useTLSIssuers(applicationID?: string) {
  return useQuery({
    queryKey: tlsIssuersQueryKey(applicationID),
    queryFn: ({ signal }) =>
      applicationID === undefined
        ? unwrap(client.GET('/tls/issuers', { signal }))
        : unwrap(
            client.GET('/applications/{id}/tls/issuers', {
              signal,
              params: { path: { id: applicationID } },
            }),
          ),
    gcTime: 0,
    retry: false,
    refetchInterval: 30000,
    refetchIntervalInBackground: false,
  })
}

export function ServiceTLS({
  application,
  service,
}: {
  application: Application
  service: string
}) {
  const scope = useScope()
  const [edit, setEdit] = useState(false)
  const tls = useQuery({
    queryKey: ['service-tls', application.id, service],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/services/{service}/tls', {
          signal,
          params: { path: { id: application.id, service } },
        }),
      ),
    gcTime: 0,
    refetchInterval: 30000,
    refetchIntervalInBackground: false,
  })
  return (
    <>
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>TLS certificate</h2>
            <HeadingHelp title="TLS certificate">
              Certificate readiness observed from the cluster.
            </HeadingHelp>
          </div>
        </div>
        {application.spec.services[service].public &&
          canAccess(scope.identity, application.project, 'deployments:write') && (
            <Button onClick={() => setEdit(true)}>
              {tls.data?.enabled ? 'Replace certificate' : 'Configure TLS'}
            </Button>
          )}
      </div>
      {tls.isPending ? (
        <Loading rows={2} />
      ) : tls.error ? (
        <ErrorState error={tls.error} />
      ) : (
        tls.data && (
          <section className="panel service-summary-panel">
            <div className="section-toolbar">
              <h3>{tls.data.hostname || 'No public hostname'}</h3>
              <Status
                value={
                  tls.data.enabled ? (tls.data.ready ? 'ready' : 'not ready') : 'not configured'
                }
              />
            </div>
            <dl className="service-definition-list [overflow-wrap:anywhere]">
              <div>
                <dt>Source</dt>
                <dd>{tls.data.source || 'None'}</dd>
              </div>
              {tls.data.issuer && (
                <div>
                  <dt>Issuer</dt>
                  <dd>
                    {tls.data.issuer}
                    {tls.data.issuer_kind === 'Issuer' && ' · Application issuer'}
                  </dd>
                </div>
              )}
              {tls.data.expires_at && (
                <div>
                  <dt>Expires</dt>
                  <dd>{timestamp(tls.data.expires_at)}</dd>
                </div>
              )}
              {tls.data.not_before && (
                <div>
                  <dt>Valid from</dt>
                  <dd>{timestamp(tls.data.not_before)}</dd>
                </div>
              )}
            </dl>
            {tls.data.message && <Note>{tls.data.message}</Note>}
          </section>
        )
      )}
      {edit && (
        <TLSForm
          key={`${application.id}:${service}`}
          application={application}
          service={service}
          hostname={tls.data?.hostname || ''}
          current={tls.data}
          onClose={() => setEdit(false)}
        />
      )}
    </>
  )
}

function TLSForm({
  application,
  service,
  hostname,
  current,
  onClose,
}: {
  application: Application
  service: string
  hostname: string
  current?: components['schemas']['TLSStatus']
  onClose: () => void
}) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const scope = useScope()
  const auth = useAuthStatus()
  const issuers = useTLSIssuers(application.id)
  const [method, setMode] = useState<TLSMethod>()
  const [issuer, setIssuer] = useState<string>()
  const [creatingIssuer, setCreatingIssuer] = useState(false)
  const [certificate, setCertificate] = useState('')
  const [privateKey, setPrivateKey] = useState('')
  const [certificateName, setCertificateName] = useState('')
  const [keyName, setKeyName] = useState('')
  const [review, setReview] = useState(false)
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const canDeploy = canAccess(scope.identity, application.project, 'deployments:write')
  const configured =
    application.spec.services[service].tls ||
    (current?.enabled
      ? {
          issuer: current.issuer,
          issuer_kind: current.issuer_kind,
          certificate: current.source === 'uploaded' ? current.secret_name || 'uploaded' : '',
        }
      : undefined)
  const cloud = dashboardEdition.cloud || auth.data?.deployment_mode === 'managed-cloud'
  const mode = preferredTLSMethod(method, configured, cloud)
  const items = issuers.data?.items || []
  const issuerValue = preferredTLSIssuer(items, issuer, configured)
  const selectedIssuer = findTLSIssuer(items, issuerValue)
  const issuerAvailable = Boolean(
    !issuers.error && !issuers.isPending && issuers.data?.installed && selectedIssuer,
  )
  const canSubmit =
    canDeploy && (mode === 'upload' ? Boolean(certificate && privateKey) : issuerAvailable)

  useEffect(() => {
    if (issuer === undefined && issuerValue) setIssuer(issuerValue)
  }, [issuer, issuerValue])

  async function file(input: File | undefined, secret: boolean) {
    if (!input) return
    setMode('upload')
    setError('')
    if (input.size > 65536) {
      setError('Choose a PEM file no larger than 64 KiB.')
      return
    }
    try {
      const value = await input.text()
      if (secret) {
        setPrivateKey(value)
        setKeyName(input.name)
      } else {
        setCertificate(value)
        setCertificateName(input.name)
      }
    } catch {
      setError('This file could not be read.')
    }
  }
  if (creatingIssuer) {
    return (
      <IssuerForm
        application={application}
        onClose={() => setCreatingIssuer(false)}
        onSaved={(created) => {
          setIssuer(tlsIssuerKey(created))
          setMode('issuer')
          setCreatingIssuer(false)
        }}
      />
    )
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!busy && !open) onClose()
      }}
      title={review ? 'Review TLS deployment' : `Configure TLS for ${service}`}
      description={`Certificate must cover ${hostname || 'this service’s public hostname'}.`}
    >
      <div className="dialog-body auth-form">
        {review ? (
          <>
            <dl className="service-definition-list [overflow-wrap:anywhere]">
              <div>
                <dt>Scope</dt>
                <dd>
                  {application.project} / {application.environment} / {service}
                </dd>
              </div>
              <div>
                <dt>Revision</dt>
                <dd>
                  r{application.revision} → r{application.revision + 1}
                </dd>
              </div>
              <div>
                <dt>Method</dt>
                <dd>
                  {mode === 'issuer'
                    ? `Managed issuer: ${selectedIssuer?.name || 'Unavailable'}`
                    : `Uploaded chain: ${certificateName}`}
                </dd>
              </div>
              {mode === 'issuer' && selectedIssuer && (
                <div>
                  <dt>Issuer scope</dt>
                  <dd>{tlsIssuerScope(selectedIssuer)}</dd>
                </div>
              )}
              {mode === 'upload' && (
                <div>
                  <dt>Private key file</dt>
                  <dd>{keyName}</dd>
                </div>
              )}
            </dl>
            {mode === 'upload' && (
              <Note>
                Certificate material is validated before deployment. Private key bytes never appear
                in configuration exports or history.
              </Note>
            )}
          </>
        ) : (
          <>
            <label>
              Certificate method
              <SelectField
                label="Certificate method"
                value={mode}
                onValueChange={(value) => setMode(value as TLSMethod)}
                options={[
                  {
                    value: 'upload',
                    label: 'Upload PEM certificate and private key',
                  },
                  {
                    value: 'issuer',
                    label: 'Use a managed ACME issuer',
                  },
                ]}
              />
            </label>
            {mode === 'upload' ? (
              <>
                <label>
                  Certificate chain (.pem or .crt)
                  <Input
                    type="file"
                    accept=".pem,.crt,.cer"
                    onChange={(e) => void file(e.target.files?.[0], false)}
                  />
                  {certificateName && (
                    <span className="field-help">Selected: {certificateName}</span>
                  )}
                </label>
                <label>
                  Private key (.pem or .key)
                  <Input
                    type="file"
                    accept=".pem,.key"
                    onChange={(e) => void file(e.target.files?.[0], true)}
                  />
                  {keyName && <span className="field-help">Selected: {keyName}</span>}
                </label>
                <p className="field-help">
                  Use an unencrypted PEM key. Files are held only while this dialog is open.
                </p>
              </>
            ) : (
              <>
                <label>
                  Issuer
                  <SelectField
                    label="Issuer"
                    value={issuerValue}
                    onValueChange={setIssuer}
                    disabled={
                      issuers.isPending || Boolean(issuers.error) || !issuers.data?.installed
                    }
                    options={[
                      {
                        value: '',
                        label: 'Choose an issuer',
                      },
                      ...(issuerValue && !selectedIssuer
                        ? [
                            {
                              value: issuerValue,
                              label: 'Selected issuer unavailable',
                              disabled: true,
                            },
                          ]
                        : []),
                      ...items.map((item) => ({
                        value: tlsIssuerKey(item),
                        label: `${item.name} · ${tlsIssuerScope(item)} · ${item.ready ? 'ready' : 'not ready'}`,
                      })),
                    ]}
                  />
                </label>
                {canDeploy && (
                  <div>
                    <Button
                      disabled={
                        issuers.isPending || Boolean(issuers.error) || !issuers.data?.installed
                      }
                      onClick={() => setCreatingIssuer(true)}
                    >
                      Create application issuer
                    </Button>
                  </div>
                )}
              </>
            )}
          </>
        )}
        {mode === 'issuer' &&
          (issuers.isPending ? (
            <Loading rows={2} />
          ) : issuers.error ? (
            <ErrorState
              title="Certificate issuers unavailable"
              error={issuers.error}
              retry={() => void issuers.refetch()}
            />
          ) : !issuers.data?.installed ? (
            <Note>
              {issuers.data?.message || 'Managed certificate issuance is unavailable.'} You can
              upload a certificate and private key instead.
            </Note>
          ) : (
            <>
              {issuers.data.message && <Note>{issuers.data.message}</Note>}
              {!items.length && (
                <Note>
                  {canDeploy
                    ? 'Create an application issuer to use managed certificates, or upload a certificate and private key.'
                    : 'No certificate issuers are available. You need deployment permission to create an application issuer.'}
                </Note>
              )}
              {issuerValue && !selectedIssuer && (
                <Note>
                  The selected issuer is unavailable. Choose another issuer before deploying.
                </Note>
              )}
              {selectedIssuer && !selectedIssuer.ready && (
                <Note>
                  {selectedIssuer.name} is not ready. Certificates cannot be issued until the issuer
                  becomes ready.
                </Note>
              )}
              {selectedIssuer?.server.includes('acme-staging') && (
                <Note>Staging certificates are not trusted by browsers.</Note>
              )}
            </>
          ))}
        {!canDeploy && (
          <Note>You need deployment permission to change this service’s certificate.</Note>
        )}
        {error && <RequestError error={error} />}
      </div>
      <div className="dialog-footer">
        <Button disabled={busy} onClick={() => (review ? setReview(false) : onClose())}>
          {review ? 'Back to configuration' : 'Cancel'}
        </Button>
        <Button
          variant="primary"
          disabled={busy || !canSubmit}
          onClick={async () => {
            if (busy || !canSubmit) return
            if (!review) {
              setMode(mode)
              setIssuer(issuerValue)
              setReview(true)
              setKey(crypto.randomUUID())
              return
            }
            setBusy(true)
            setError('')
            try {
              const result = await unwrap(
                client.POST('/applications/{id}/services/{service}/tls', {
                  params: {
                    path: { id: application.id, service },
                    header: { 'Idempotency-Key': key },
                  },
                  body: {
                    expected_revision: application.revision,
                    ...(mode === 'issuer'
                      ? { issuer: selectedIssuer!.name, issuer_kind: selectedIssuer!.kind }
                      : { certificate_pem: certificate, private_key_pem: privateKey }),
                  },
                }),
              )
              setPrivateKey('')
              setCertificate('')
              void cache.invalidateQueries({ queryKey: ['application', application.id] })
              onClose()
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
          {busy ? 'Submitting…' : review ? 'Deploy TLS change' : 'Review TLS change'}
        </Button>
      </div>
    </Dialog>
  )
}

export default function IssuerSettings({ application }: { application?: Application }) {
  const scope = useScope()
  const access = useInstallationAccess()
  const [create, setCreate] = useState(false)
  const issuers = useTLSIssuers(application?.id)
  const canCreate = application
    ? canAccess(scope.identity, application.project, 'deployments:write')
    : access.allowed
  return (
    <>
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>Certificate issuers</h2>
            <HeadingHelp title="Certificate issuers">
              {application
                ? 'Use the configured default or create an issuer for this application. Select an issuer from a public service’s Networking tab.'
                : 'Managed ACME issuance through the installed cert-manager controller.'}
            </HeadingHelp>
          </div>
        </div>
        {canCreate && (
          <Button
            variant="primary"
            disabled={issuers.isPending || Boolean(issuers.error) || !issuers.data?.installed}
            onClick={() => setCreate(true)}
          >
            {application ? 'Create application issuer' : 'Create issuer'}
          </Button>
        )}
      </div>
      {issuers.isPending ? (
        <Loading />
      ) : issuers.error ? (
        <ErrorState
          title="Certificate issuers unavailable"
          error={issuers.error}
          retry={() => void issuers.refetch()}
        />
      ) : !issuers.data?.installed ? (
        <Empty
          icon="lock"
          title="Managed certificates unavailable"
          description={
            issuers.data?.message ||
            'Managed issuance needs a certificate controller. You can upload a PEM certificate and private key on each public service.'
          }
        />
      ) : (
        <>
          {issuers.data.message && <Note>{issuers.data.message}</Note>}
          {!issuers.data.items.length ? (
            <Empty
              icon="lock"
              title="No issuers configured"
              description={
                canCreate
                  ? 'Create a staging issuer to validate DNS and HTTP challenge routing.'
                  : application
                    ? 'You need deployment permission to create an application issuer.'
                    : 'An installation operator can create a shared issuer.'
              }
            />
          ) : (
            <div className="divide-y divide-border">
              {issuers.data.items.map((issuer) => (
                <div
                  className="flex min-w-0 items-start justify-between gap-3 py-3"
                  key={tlsIssuerKey(issuer)}
                >
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
                      <strong className="text-sm [overflow-wrap:anywhere]">{issuer.name}</strong>
                      <span className="text-xs text-muted-foreground">
                        {tlsIssuerScope(issuer)}
                      </span>
                    </div>
                    {(issuer.email || issuer.server || issuer.conditions.length > 0) && (
                      <details className="mt-2 text-xs text-muted-foreground">
                        <summary className="w-fit cursor-pointer">Issuer details</summary>
                        <dl className="service-definition-list mt-2 [overflow-wrap:anywhere]">
                          {issuer.email && (
                            <div>
                              <dt>Contact email</dt>
                              <dd>{issuer.email}</dd>
                            </div>
                          )}
                          {issuer.server && (
                            <div>
                              <dt>ACME server</dt>
                              <dd>{issuer.server}</dd>
                            </div>
                          )}
                        </dl>
                        {issuer.conditions.map((condition, index) => (
                          <p
                            className="field-help [overflow-wrap:anywhere]"
                            key={`${condition.type}:${index}`}
                          >
                            {condition.type}: {condition.status} ·{' '}
                            {condition.message || condition.reason}
                          </p>
                        ))}
                      </details>
                    )}
                  </div>
                  <Status value={issuer.ready ? 'ready' : 'not ready'} />
                </div>
              ))}
            </div>
          )}
        </>
      )}
      {create && (
        <IssuerForm
          key={application?.id || 'installation'}
          application={application}
          onClose={() => setCreate(false)}
          onSaved={() => {
            setCreate(false)
          }}
        />
      )}
    </>
  )
}

function IssuerForm({
  application,
  onClose,
  onSaved,
}: {
  application?: Application
  onClose: () => void
  onSaved: (issuer: TLSIssuer) => void
}) {
  const cache = useQueryClient()
  const scope = useScope()
  const access = useInstallationAccess()
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [production, setProduction] = useState(false)
  const [review, setReview] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const canCreate = application
    ? canAccess(scope.identity, application.project, 'deployments:write')
    : access.allowed
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!busy && !open) onClose()
      }}
      title={
        review
          ? 'Review certificate issuer'
          : application
            ? 'Create application issuer'
            : 'Create certificate issuer'
      }
      description="Start with staging to verify domain challenge routing."
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          if (busy || !canCreate) return
          if (!review) {
            setReview(true)
            return
          }
          setBusy(true)
          setError('')
          try {
            const body = { name, email, production }
            const created = await unwrap(
              application
                ? client.POST('/applications/{id}/tls/issuers', {
                    params: { path: { id: application.id } },
                    body,
                  })
                : client.POST('/tls/issuers', { body }),
            )
            const queryKey = tlsIssuersQueryKey(application?.id)
            await cache.cancelQueries({ queryKey, exact: true })
            cache.setQueryData<TLSIssuers>(queryKey, (previous) => ({
              ...previous,
              installed: true,
              items: [
                ...(previous?.items || []).filter(
                  (item) => tlsIssuerKey(item) !== tlsIssuerKey(created),
                ),
                created,
              ],
            }))
            void cache.invalidateQueries({ queryKey, exact: true })
            onSaved(created)
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="dialog-body auth-form">
          {review ? (
            <dl className="service-definition-list [overflow-wrap:anywhere]">
              <div>
                <dt>Scope</dt>
                <dd>
                  {application
                    ? `${application.project} / ${application.environment} / ${application.spec.name}`
                    : 'Installation'}
                </dd>
              </div>
              <div>
                <dt>Issuer</dt>
                <dd>{name}</dd>
              </div>
              <div>
                <dt>Contact email</dt>
                <dd>{email}</dd>
              </div>
              <div>
                <dt>ACME environment</dt>
                <dd>{production ? 'Let’s Encrypt production' : 'Let’s Encrypt staging'}</dd>
              </div>
            </dl>
          ) : (
            <>
              <label>
                Issuer name
                <Input
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  pattern="[a-z]([a-z0-9\-]*[a-z0-9])?"
                  maxLength={63}
                  required
                />
              </label>
              <label>
                ACME contact email
                <Input
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  maxLength={254}
                  required
                />
              </label>
              <label className="checkbox-row">
                <Input
                  type="checkbox"
                  checked={production}
                  onChange={(e) => setProduction(e.target.checked)}
                />
                Use production certificate issuance
              </label>
            </>
          )}
          <Note>
            {production
              ? 'This creates a production ACME issuer. Use valid public DNS and working HTTP challenge routing.'
              : 'Staging certificates validate the flow but are not trusted by browsers.'}
          </Note>
          {!canCreate && (
            <Note>
              {application
                ? 'You need deployment permission to create an application issuer.'
                : 'Only an installation operator can create a shared issuer.'}
            </Note>
          )}
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button
            type="button"
            disabled={busy}
            onClick={() => (review ? setReview(false) : onClose())}
          >
            {review ? 'Back' : 'Cancel'}
          </Button>
          <Button type="submit" variant="primary" disabled={busy || !canCreate}>
            {busy ? 'Creating…' : review ? 'Create reviewed issuer' : 'Review issuer'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
