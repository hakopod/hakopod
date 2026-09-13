import { useEffect, useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import type { Application, Plan } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import { FormPage, FormSection, FormHint } from './form-page'
import { DiffTable } from './deploy-dialog'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Note } from './shared'

export function BackendCertificateForm({
  application,
  serviceName,
}: {
  application: Application
  serviceName: string
}) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [source, setSource] = useState('upload')
  const [hostname, setHostname] = useState('')
  const [mountPath, setMountPath] = useState('/app/certificates')
  const [certificate, setCertificate] = useState<File | null>(null)
  const [privateKey, setPrivateKey] = useState<File | null>(null)
  const [uploaded, setUploaded] = useState<{ certificate: string; hostname: string } | null>(null)
  const [plan, setPlan] = useState<Plan | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const request = useRef<AbortController | null>(null)
  const idempotency = useRef('')
  useEffect(() => () => request.current?.abort(), [])
  const back = () =>
    void navigate({
      to: '/applications/$applicationId',
      params: { applicationId: application.id },
      search: { service: serviceName, tab: 'network' },
    })
  async function review() {
    if (busy) return
    setBusy(true)
    setError('')
    const controller = new AbortController()
    request.current = controller
    try {
      let reference = uploaded
      if (!reference) {
        if (source === 'upload' && (!certificate || !privateKey))
          throw new Error('Choose the certificate chain and its private key.')
        if (
          source === 'upload' &&
          ((certificate?.size || 0) > 256 * 1024 || (privateKey?.size || 0) > 32 * 1024)
        )
          throw new Error(
            'The certificate chain must be at most 256 KiB and the key at most 32 KiB.',
          )
        reference = await unwrap(
          client.POST('/applications/{id}/services/{service}/certificates', {
            signal: controller.signal,
            params: { path: { id: application.id, service: serviceName } },
            body:
              source === 'ingress'
                ? { hostname, from_ingress: true }
                : {
                    hostname,
                    certificate_pem: await certificate!.text(),
                    private_key_pem: await privateKey!.text(),
                  },
          }),
        )
        setUploaded(reference)
        void cache.invalidateQueries({
          queryKey: ['backend-certificates', application.id, serviceName],
        })
      }
      const current = await unwrap(
        client.GET('/applications/{id}', {
          signal: controller.signal,
          params: { path: { id: application.id } },
        }),
      )
      if (current.revision !== application.revision)
        throw new Error(
          'The application changed while this form was open. Your uploaded certificate is saved. Return to the service and review against its latest revision.',
        )
      const spec = structuredClone(current.spec)
      const service = spec.services[serviceName]
      if (!service) throw new Error('This service is no longer present.')
      service.certificate_mounts = [
        ...(service.certificate_mounts || []).filter((item) => item.mount_path !== mountPath),
        { certificate: reference.certificate, hostname: reference.hostname, mount_path: mountPath },
      ]
      const result = await unwrap(
        client.POST('/plan', {
          signal: controller.signal,
          body: {
            project: current.project,
            environment: current.environment,
            service: serviceName,
            spec,
          },
        }),
      )
      if (result.application_id !== current.id || result.expected_revision !== current.revision)
        throw new Error('The application changed during review. Review again before deployment.')
      setPlan(result)
      idempotency.current = crypto.randomUUID()
    } catch (cause) {
      if (!controller.signal.aborted) setError(message(cause))
    } finally {
      if (!controller.signal.aborted) setBusy(false)
    }
  }
  async function deploy() {
    if (!plan || busy) return
    setBusy(true)
    setError('')
    const controller = new AbortController()
    request.current = controller
    try {
      const result = await unwrap(
        client.POST('/deployments', {
          signal: controller.signal,
          body: {
            project: application.project,
            environment: application.environment,
            service: serviceName,
            spec: plan.spec,
            expected_revision: plan.expected_revision,
          },
          params: { header: { 'Idempotency-Key': idempotency.current } },
        }),
      )
      void cache.invalidateQueries({ queryKey: ['application', application.id] })
      void navigate({ to: '/deployments/$deploymentId', params: { deploymentId: result.id } })
    } catch (cause) {
      if (!controller.signal.aborted) {
        setError(message(cause))
        if (cause instanceof APIError && cause.status === 409) setPlan(null)
      }
    } finally {
      if (!controller.signal.aborted) setBusy(false)
    }
  }
  return (
    <FormPage
      title={plan ? 'Review certificate mount' : `${serviceName} certificate`}
      description="Mount a service certificate for STARTTLS or another application protocol."
      breadcrumbs={[]}
      help={
        <FormHint title="Rotation and permissions">
          The mount contains tls.crt and tls.key. Files are read-only and readable by the service’s
          filesystem group. Each rotation uses a new certificate reference and deployment; your
          application loads it on startup.
        </FormHint>
      }
    >
      {plan ? (
        <FormSection title="Deployment changes">
          <Note>
            This replaces the service’s pods. The application must load its certificate from{' '}
            {mountPath}. Review the mount and any other changes before deployment.
          </Note>
          <DiffTable changes={plan.changes} />
          {plan.warnings.map((warning) => (
            <Note key={warning}>{warning}</Note>
          ))}
        </FormSection>
      ) : (
        <FormSection title="Certificate source">
          <SelectField
            label="Source"
            value={source}
            disabled={busy}
            onValueChange={(value) => {
              setSource(value)
              setUploaded(null)
            }}
            options={[
              { value: 'upload', label: 'Upload PEM files' },
              {
                value: 'ingress',
                label: application.spec.services[serviceName].public
                  ? 'Copy current service ingress certificate'
                  : 'Ingress copy requires public HTTP',
                disabled: !application.spec.services[serviceName].public,
              },
            ]}
          />
          <label className="field">
            Hostname
            <Input
              value={hostname}
              placeholder="smtp.example.com"
              disabled={busy}
              onChange={(event) => {
                setHostname(event.target.value)
                setUploaded(null)
              }}
              autoComplete="off"
            />
          </label>
          <label className="field">
            Mount directory
            <Input
              value={mountPath}
              disabled={busy}
              onChange={(event) => setMountPath(event.target.value)}
              autoComplete="off"
            />
          </label>
          {source === 'upload' ? (
            <div className="form-grid-two">
              <label className="field">
                Certificate chain (.pem)
                <Input
                  type="file"
                  accept=".pem,.crt"
                  disabled={busy}
                  onChange={(event) => {
                    setCertificate(event.target.files?.[0] || null)
                    setUploaded(null)
                  }}
                />
                {certificate && <span className="muted-text">Selected: {certificate.name}</span>}
              </label>
              <label className="field">
                Private key (.pem)
                <Input
                  type="file"
                  accept=".pem,.key"
                  disabled={busy}
                  onChange={(event) => {
                    setPrivateKey(event.target.files?.[0] || null)
                    setUploaded(null)
                  }}
                />
                {privateKey && <span className="muted-text">Selected: {privateKey.name}</span>}
              </label>
            </div>
          ) : (
            <Note>
              Copies the currently issued certificate from this service’s HTTP ingress. The copy
              does not follow later renewals; import and deploy again after renewal.
            </Note>
          )}
          <Note>
            Review saves a service-scoped certificate in Kubernetes. It does not change the running
            service. The private key never enters deployment history.
          </Note>
          {uploaded && (
            <Note>
              Certificate saved: <code>{uploaded.certificate}</code>. Continue reviewing its mount.
            </Note>
          )}
        </FormSection>
      )}
      {error && (
        <div role="alert">
          <Note>{error}</Note>
        </div>
      )}
      <div className="form-footer">
        <span className="dialog-footer-note">
          {plan
            ? 'Apply only after reviewing the changes'
            : 'Service changes start only when you deploy'}
        </span>
        <div className="button-row">
          <Button variant="ghost" disabled={busy} onClick={plan ? () => setPlan(null) : back}>
            {plan ? 'Back to certificate' : 'Cancel'}
          </Button>
          <Button
            disabled={busy || (!plan && (!hostname || !mountPath))}
            onClick={() => void (plan ? deploy() : review())}
          >
            {busy ? 'Working…' : plan ? 'Deploy certificate' : 'Upload and review'}
          </Button>
        </div>
      </div>
    </FormPage>
  )
}
