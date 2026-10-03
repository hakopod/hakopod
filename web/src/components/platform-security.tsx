import { useState } from 'react'
import type { ManagedPlatform } from '../lib/managed-platforms'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { Button } from './ui/button'
import { Copy, HeadingHelp, Note } from './shared'

export function PlatformSecurity({
  platform,
  stale,
}: {
  platform: ManagedPlatform
  stale: boolean
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const managed = platform.spec.tls_mode === 'managed'
  const tls = platform.observation.tls
  const maintenance = platform.observation.maintenance
  const previousRevision = platform.observation.revision !== platform.revision
  const certificates = tls?.certificates || []
  const expired = certificates.some(
    (certificate) => Date.parse(certificate.expires_at) <= Date.now(),
  )
  async function download() {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      const trust = await unwrap(
        client.GET('/managed-platforms/{id}/trust', { params: { path: { id: platform.id } } }),
      )
      const url = URL.createObjectURL(
        new Blob([trust.certificate_pem], { type: 'application/x-pem-file' }),
      )
      const link = document.createElement('a')
      link.href = url
      link.download = `${platform.spec.name}-ca.crt`
      link.click()
      window.setTimeout(() => URL.revokeObjectURL(url), 1000)
    } catch (failure) {
      setError(message(failure))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="db-panel" aria-label="Connection security">
      <div className="db-panel-heading">
        <div>
          <h2>Connection security</h2>
          <HeadingHelp title="platform connection security">
            Hakopod checks each managed endpoint's certificate against its hostname and the
            platform's certificate authority. The download contains only the public CA.
          </HeadingHelp>
        </div>
        {managed && (
          <Button onClick={() => void download()} disabled={busy || !tls?.verified_at}>
            {busy ? 'Downloading…' : 'Download public CA'}
          </Button>
        )}
      </div>
      <div className="px-4 pb-4">
        {managed && maintenance && maintenance.status !== 'succeeded' && (
          <Note>
            {maintenance.status === 'failed'
              ? 'Certificate maintenance failed.'
              : 'Certificate maintenance is pending.'}{' '}
            {maintenance.message} Last checked {timestamp(maintenance.checked_at)}.
          </Note>
        )}
        {!managed ? (
          <Note>
            Your operator supplies and renews this platform's certificates. Obtain its public CA
            from your operator before connecting.
          </Note>
        ) : expired ? (
          <Note>
            An observed certificate has expired. Check renewal and verify the endpoint before
            connecting.
          </Note>
        ) : !tls?.verified_at ? (
          <Note>Waiting for the first certificate verification.</Note>
        ) : stale || previousRevision ? (
          <Note>
            Showing an earlier certificate check. Refresh to check the latest platform state.
          </Note>
        ) : null}
        <dl className="db-facts py-3">
          <div>
            <dt>Certificate management</dt>
            <dd>{managed ? 'Hakopod' : 'Operator'}</dd>
          </div>
          <div>
            <dt>Last verified</dt>
            <dd>{tls?.verified_at ? timestamp(tls.verified_at) : 'Not observed'}</dd>
          </div>
          {tls?.issuer_fingerprint && (
            <div className="col-span-full">
              <dt>CA fingerprint · SHA-256</dt>
              <dd className="flex items-start gap-2">
                <code className="min-w-0 break-all">{tls.issuer_fingerprint}</code>
                <Copy
                  iconOnly
                  value={tls.issuer_fingerprint}
                  label="Copy platform CA fingerprint"
                />
              </dd>
            </div>
          )}
        </dl>
        {managed && (
          <p className="text-sm text-muted-foreground">
            Install this CA in your client and verify the endpoint hostname. A restored platform has
            its own CA; update client trust before switching to it.
          </p>
        )}
        {error && (
          <p role="alert" className="mt-3 text-sm text-destructive">
            {error}
          </p>
        )}
      </div>
      {certificates.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-left text-sm">
            <caption className="sr-only">Certificates at the last verification</caption>
            <thead>
              <tr className="border-y border-border">
                <th className="p-3 font-medium">Component</th>
                <th className="p-3 font-medium">Last check</th>
                <th className="p-3 font-medium">Expires</th>
              </tr>
            </thead>
            <tbody>
              {certificates.map((certificate) => (
                <tr key={certificate.component} className="border-b border-border last:border-0">
                  <th className="p-3 font-normal break-all">{certificate.component}</th>
                  <td className="p-3">
                    {Date.parse(certificate.expires_at) <= Date.now()
                      ? 'Expired'
                      : certificate.verified
                        ? 'Verified'
                        : 'Not verified'}
                  </td>
                  <td className="p-3">{timestamp(certificate.expires_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}
