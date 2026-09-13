import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { HeadingHelp, Empty, ErrorState, Loading, Note, Status } from './shared'

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
        {application.spec.services[service].public && scope.can('deployments:write') && (
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
            <dl className="service-definition-list">
              <div>
                <dt>Source</dt>
                <dd>{tls.data.source || 'None'}</dd>
              </div>
              {tls.data.issuer && (
                <div>
                  <dt>Issuer</dt>
                  <dd>{tls.data.issuer}</dd>
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
          application={application}
          service={service}
          hostname={tls.data?.hostname || ''}
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
  onClose,
}: {
  application: Application
  service: string
  hostname: string
  onClose: () => void
}) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [mode, setMode] = useState<'upload' | 'issuer'>('upload')
  const [issuer, setIssuer] = useState('')
  const [certificate, setCertificate] = useState('')
  const [privateKey, setPrivateKey] = useState('')
  const [certificateName, setCertificateName] = useState('')
  const [keyName, setKeyName] = useState('')
  const [review, setReview] = useState(false)
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const issuers = useQuery({
    queryKey: ['tls-issuers'],
    queryFn: ({ signal }) => unwrap(client.GET('/tls/issuers', { signal })),
    enabled: mode === 'issuer',
    gcTime: 0,
  })
  async function file(input: File | undefined, secret: boolean) {
    if (!input) return
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
            <dl className="service-definition-list">
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
                    ? `Managed issuer: ${issuer}`
                    : `Uploaded chain: ${certificateName}`}
                </dd>
              </div>
              {mode === 'upload' && (
                <div>
                  <dt>Private key file</dt>
                  <dd>{keyName}</dd>
                </div>
              )}
            </dl>
            <Note>
              The backend validates certificate material before accepting a deployment. Private key
              bytes never appear in configuration exports or history.
            </Note>
          </>
        ) : (
          <>
            <label>
              Certificate method
              <SelectField
                label="Certificate method"
                value={mode}
                onValueChange={(value) => setMode(value as 'upload' | 'issuer')}
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
                </label>
                <label>
                  Private key (.pem or .key)
                  <Input
                    type="file"
                    accept=".pem,.key"
                    onChange={(e) => void file(e.target.files?.[0], true)}
                  />
                </label>
                <p className="field-help">
                  Use an unencrypted PEM key. Files are held only while this dialog is open.
                </p>
              </>
            ) : issuers.isPending ? (
              <Loading rows={2} />
            ) : issuers.error ? (
              <ErrorState error={issuers.error} />
            ) : !issuers.data?.installed ? (
              <Note>
                {issuers.data?.message ||
                  'Install cert-manager before configuring managed certificates.'}
              </Note>
            ) : (
              <>
                <label>
                  Issuer
                  <SelectField
                    label="Issuer"
                    value={issuer}
                    onValueChange={(value) => setIssuer(value)}
                    options={[
                      {
                        value: '',
                        label: 'Choose an issuer',
                      },
                      ...(issuers.data.items.map((item) => ({
                        value: item.name,
                        label: item.name + ' · ' + (item.ready ? 'ready' : 'not ready'),
                      })) ?? []),
                    ]}
                  />
                </label>
                {!issuers.data.items.length && (
                  <Note>An administrator must create a certificate issuer in Infrastructure.</Note>
                )}
                <Note>
                  Staging issuers are useful for validation; browsers do not trust staging
                  certificates.
                </Note>
              </>
            )}
          </>
        )}
        {error && (
          <div className="inline-error" role="alert">
            {error}
          </div>
        )}
      </div>
      <div className="dialog-footer">
        <Button disabled={busy} onClick={() => (review ? setReview(false) : onClose())}>
          {review ? 'Back to configuration' : 'Cancel'}
        </Button>
        <Button
          variant="primary"
          disabled={busy || (mode === 'upload' ? !certificate || !privateKey : !issuer)}
          onClick={async () => {
            if (!review) {
              setReview(true)
              setKey(crypto.randomUUID())
              return
            }
            if (busy) return
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
                      ? { issuer }
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

export default function IssuerSettings() {
  const scope = useScope()
  const [create, setCreate] = useState(false)
  const issuers = useQuery({
    queryKey: ['tls-issuers'],
    queryFn: ({ signal }) => unwrap(client.GET('/tls/issuers', { signal })),
    gcTime: 0,
    refetchInterval: 30000,
    refetchIntervalInBackground: false,
  })
  return (
    <>
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>Certificate issuers</h2>
            <HeadingHelp title="Certificate issuers">
              Managed ACME issuance through the installed cert-manager controller.
            </HeadingHelp>
          </div>
        </div>
        {scope.identity.admin && (
          <Button
            variant="primary"
            disabled={!issuers.data?.installed}
            onClick={() => setCreate(true)}
          >
            Create issuer
          </Button>
        )}
      </div>
      {issuers.isPending ? (
        <Loading />
      ) : issuers.error ? (
        <ErrorState error={issuers.error} />
      ) : !issuers.data?.installed ? (
        <Empty
          icon="lock"
          title="cert-manager is not installed"
          description={
            issuers.data?.message ||
            'Install and configure cert-manager to use managed certificate issuers. PEM uploads remain available per public service.'
          }
        />
      ) : !issuers.data.items.length ? (
        <Empty
          icon="lock"
          title="No issuers configured"
          description="Create a staging issuer to validate DNS and HTTP challenge routing."
        />
      ) : (
        <div className="panel settings-session-list">
          {issuers.data.items.map((issuer) => (
            <div className="settings-list-row" key={issuer.name}>
              <div>
                <strong>{issuer.name}</strong>
                <small>
                  {issuer.email} · {issuer.server}
                </small>
                {issuer.conditions.map((condition) => (
                  <p className="field-help" key={condition.type}>
                    {condition.type}: {condition.status} · {condition.message || condition.reason}
                  </p>
                ))}
              </div>
              <Status value={issuer.ready ? 'ready' : 'not ready'} />
            </div>
          ))}
        </div>
      )}
      {create && (
        <IssuerForm
          onClose={() => setCreate(false)}
          onSaved={() => {
            setCreate(false)
            void issuers.refetch()
          }}
        />
      )}
    </>
  )
}

function IssuerForm({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [production, setProduction] = useState(false)
  const [review, setReview] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!busy && !open) onClose()
      }}
      title={review ? 'Review certificate issuer' : 'Create certificate issuer'}
      description="Start with staging to verify domain challenge routing."
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          if (busy) return
          if (!review) {
            setReview(true)
            return
          }
          setBusy(true)
          setError('')
          try {
            await unwrap(client.POST('/tls/issuers', { body: { name, email, production } }))
            onSaved()
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="dialog-body auth-form">
          {review ? (
            <dl className="service-definition-list">
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
                  pattern="[a-z][a-z0-9-]*"
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
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button
            type="button"
            disabled={busy}
            onClick={() => (review ? setReview(false) : onClose())}
          >
            {review ? 'Back' : 'Cancel'}
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Creating…' : review ? 'Create reviewed issuer' : 'Review issuer'}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}
