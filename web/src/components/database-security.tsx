import { useState, type ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import type { ManagedDatabase } from '../lib/databases'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { Button } from './ui/button'
import { Copy, HeadingHelp, RequestError, Status } from './shared'
import { canAccess, useScope } from '../lib/scope'

export function DatabaseSecurity({ database: d, now }: { database: ManagedDatabase; now: number }) {
  const { identity } = useScope()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const tls = d.observation.tls
  const checked = Date.parse(tls?.checked_at || '')
  const fresh =
    d.observation.revision === d.revision &&
    Number.isFinite(checked) &&
    checked <= now &&
    now - checked <= 30_000
  const required = d.spec.tls?.mode === 'required'
  const expires = Date.parse(tls?.expires_at || '')
  const expired = required && Number.isFinite(expires) && expires <= now
  const verified = required && fresh && tls?.verified && tls.plaintext_rejected && expires > now
  const canManage = !identity.application && canAccess(identity, d.project, 'deployments:write')
  const status = expired
    ? 'expired'
    : verified
      ? tls?.message
        ? 'warning'
        : 'verified'
      : required
        ? tls?.checked_at && !fresh
          ? 'stale'
          : 'pending'
        : 'unenforced'
  const notice = !required
    ? 'Client TLS is not enforced. Recover into a database with required TLS and review application connections before cutover.'
    : expired
      ? 'The last observed server certificate has expired. Check renewal and verify the endpoint before connecting.'
      : !verified || tls?.message
        ? tls?.message ||
          (tls?.checked_at && !fresh
            ? 'TLS verification is stale. Waiting for a current connection check.'
            : 'Waiting for verified TLS and plaintext rejection checks.')
        : ''
  const facts: [string, ReactNode][] = [
    ['Client policy', required ? 'TLS required' : 'Legacy policy'],
    ['Plaintext connections', verified ? 'Rejected' : 'Not currently verified'],
    ['Minimum protocol', tls?.minimum_version || 'Not observed'],
    ['Last checked', tls?.checked_at ? timestamp(tls.checked_at) : 'Not checked'],
    ['Certificate issuer', tls?.issuer || 'Not observed'],
    ['Certificate expires', tls?.expires_at ? timestamp(tls.expires_at) : 'Not observed'],
  ]
  if (tls?.dns_names?.length) facts.push(['Certificate names', tls.dns_names.join(', ')])
  return (
    <section className="app-workspace-section" aria-labelledby="db-security-heading">
      <div className="app-workspace-section-head">
        <div className="db-section-title">
          <h2 id="db-security-heading">Connection security</h2>
          <HeadingHelp title="connection security">
            Verification checks the certificate issuer, endpoint hostname and validity, then tests
            an encrypted connection and rejects plaintext. Download contains only the public CA.
          </HeadingHelp>
          <Status value={status} small />
        </div>
        {required && (
          <Button
            size="sm"
            disabled={busy}
            aria-busy={busy || undefined}
            onClick={async () => {
              setBusy(true)
              setError('')
              try {
                const trust = await unwrap(
                  client.GET('/databases/{id}/trust', { params: { path: { id: d.id } } }),
                )
                const url = URL.createObjectURL(
                  new Blob([trust.certificate_pem], { type: 'application/x-pem-file' }),
                )
                const link = document.createElement('a')
                link.href = url
                link.download = `${d.spec.name}-ca.crt`
                link.click()
                setTimeout(() => URL.revokeObjectURL(url), 1000)
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Downloading…' : 'Download public CA'}
          </Button>
        )}
        {required && d.spec.engine === 'postgresql' && canManage && (
          <Button asChild size="sm">
            <Link
              to="/databases/$databaseId/public-endpoints"
              params={{ databaseId: d.id }}
              search={{ project: d.project, environment: d.environment }}
            >
              Public endpoints
            </Link>
          </Button>
        )}
      </div>
      {notice && (
        <p className="db-section-note" role="status">
          {notice}
        </p>
      )}
      {required && clientGuidance(d) && <p className="db-section-note db-requirement">{clientGuidance(d)}</p>}
      {error && <RequestError error={error} />}
      <dl className="app-workspace-details">
        {facts.map(([term, value]) => (
          <div key={term}>
            <dt>{term}</dt>
            <dd>{value}</dd>
          </div>
        ))}
        {tls?.ca_fingerprint && (
          <div>
            <dt>CA fingerprint · SHA-256</dt>
            <dd className="db-fingerprint">
              <code>{tls.ca_fingerprint}</code>
              <Copy iconOnly value={tls.ca_fingerprint} label="Copy CA fingerprint" />
            </dd>
          </div>
        )}
        {tls?.fingerprint && (
          <div>
            <dt>Server fingerprint · SHA-256</dt>
            <dd className="db-fingerprint">
              <code>{tls.fingerprint}</code>
              <Copy iconOnly value={tls.fingerprint} label="Copy server fingerprint" />
            </dd>
          </div>
        )}
      </dl>
    </section>
  )
}

/** How a client should trust the endpoint, per engine; kept in help beside the heading. */
function clientGuidance(d: ManagedDatabase) {
  const replicas = d.spec.replicas > 0
  switch (d.spec.engine) {
    case 'vitess':
      return `vtgate accepts MySQL TLS on port 3306. Use app@primary${replicas ? ' or app@replica' : ''} as the database target, with the mounted public CA and verified hostname.${replicas ? ' Replica reads may lag.' : ''} Tablets, topology members and control services do not expose application credentials or cluster access.`
    case 'mysql':
      return 'Application bindings mount the public CA. Configure your MySQL driver to use it and verify the endpoint hostname. The MySQL CLI requires --ssl-mode=VERIFY_IDENTITY and --ssl-ca pointing to that CA.'
    case 'mongodb':
      return 'Bindings mount the public CA and configure TLS, hostname verification, replica-set discovery and majority reads and writes. The driver must reach every advertised member. Standalone also uses a replica-set connection.'
    case 'clickhouse':
      return 'Bindings use native ClickHouse TLS on port 9440. Configure the driver with the mounted public CA and endpoint hostname. A URL alone does not install private CA trust.'
    case 'oracle':
      return 'Connect with TCPS on port 2484 to the FREEPDB1 service as APP. Configure your driver or client wallet with the public CA and verify the endpoint hostname. Never copy the server wallet or private key to an application.'
    case 'postgresql':
      return 'Managed application bindings mount the public CA and use sslmode=verify-full. For external clients, set sslrootcert to the downloaded CA and connect using the endpoint hostname.'
    default:
      return ''
  }
}
