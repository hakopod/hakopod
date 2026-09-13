import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Copy, ErrorState, HeadingHelp, Loading, Note } from './shared'

export function ServiceDelivery({
  application,
  serviceName,
}: {
  application: Application
  serviceName: string
}) {
  const scope = useScope()
  const service = application.spec.services[serviceName]
  const params = { path: { id: application.id, service: serviceName } }
  const delivery = useQuery({
    queryKey: ['service-delivery', application.id, serviceName, application.revision],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}/services/{service}/delivery', { signal, params })),
    refetchInterval: 15000,
    refetchIntervalInBackground: false,
    gcTime: 0,
  })
  const certificates = useQuery({
    queryKey: ['backend-certificates', application.id, serviceName, application.revision],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}/services/{service}/certificates', { signal, params })),
    gcTime: 0,
  })
  return (
    <section className="panel service-summary-panel service-delivery">
      <div className="panel-heading">
        <div className="delivery-title">
          <h2>TCP and workload access</h2>
          <HeadingHelp title="TCP and workload access">
            TCP connections pass through unchanged. The service handles STARTTLS. Certificate and
            identity references use the same application TOML.
          </HeadingHelp>
        </div>
        {scope.can('deployments:write') && (
          <Button asChild size="sm" variant="ghost">
            <Link
              to="/applications/$applicationId/certificates"
              params={{ applicationId: application.id }}
              search={{ service: serviceName }}
            >
              Add certificate
            </Link>
          </Button>
        )}
      </div>
      {delivery.isPending ? (
        <Loading rows={2} />
      ) : delivery.error ? (
        <ErrorState error={delivery.error} />
      ) : (
        <>
          {delivery.data?.public_tcp_policy && (
            <p className="muted-text">{delivery.data.public_tcp_policy.message}</p>
          )}
          {delivery.data?.public_tcp.length ? (
            <ul className="delivery-list" aria-label="Public TCP listeners">
              {delivery.data.public_tcp.map((listener) => (
                <li key={listener.port}>
                  <div className="section-toolbar">
                    <strong>
                      TCP {listener.port} → {listener.target_port}
                    </strong>
                    <span className="label-chip">{listener.status}</span>
                  </div>
                  <span className="muted-text">
                    Allowed sources:{' '}
                    {service.public_tcp
                      ?.find((item) => item.port === listener.port)
                      ?.source_cidrs.join(', ')}
                  </span>
                  {listener.message !== delivery.data.public_tcp_policy?.message && (
                    <p>{listener.message}</p>
                  )}
                  {listener.addresses?.map((address) => (
                    <span className="copyable-address" key={address}>
                      <code>{address}</code>
                      <Copy value={address} />
                    </span>
                  ))}
                </li>
              ))}
            </ul>
          ) : delivery.data?.public_tcp_policy?.allowed !== false ? (
            <p className="muted-text">
              No public TCP listeners. Extra service ports remain private.
            </p>
          ) : null}
          {service.aws_identity && (
            <dl className="service-definition-list">
              <div>
                <dt>AWS binding</dt>
                <dd>{service.aws_identity}</dd>
              </div>
              <div>
                <dt>Identity state</dt>
                <dd>{delivery.data?.aws_identity?.status || 'Unavailable'}</dd>
              </div>
              {delivery.data?.aws_identity && (
                <>
                  <div>
                    <dt>Role</dt>
                    <dd>
                      <code>{delivery.data.aws_identity.role_arn}</code>
                    </dd>
                  </div>
                  <div>
                    <dt>Region</dt>
                    <dd>{delivery.data.aws_identity.region}</dd>
                  </div>
                </>
              )}
            </dl>
          )}
          {service.aws_identity && (
            <Note>
              {delivery.data?.aws_identity?.message ||
                'Identity preparation does not verify AWS access. Test the intended AWS operation before moving production traffic.'}
            </Note>
          )}
          <span className="muted-text">Observed {timestamp(delivery.data?.observed_at)}</span>
        </>
      )}
      <div className="delivery-title">
        <h3>Backend certificates</h3>
        <HeadingHelp title="Backend certificates">
          Only mounted certificates are listed here. Uploads stay separate from deployment. Rotation
          requires a new reference and reviewed deployment.
        </HeadingHelp>
      </div>
      {certificates.isPending ? (
        <Loading rows={1} />
      ) : certificates.error ? (
        <ErrorState error={certificates.error} />
      ) : service.certificate_mounts?.length ? (
        <ul className="delivery-list" aria-label="Mounted certificates">
          {service.certificate_mounts.map((mount) => {
            const status = certificates.data?.items.find(
              (item) => item.certificate === mount.certificate,
            )
            return (
              <li key={mount.mount_path}>
                <div className="section-toolbar">
                  <strong>{mount.hostname}</strong>
                  <span className="label-chip">
                    {status?.ready ? 'Valid certificate' : 'Needs attention'}
                  </span>
                </div>
                <div className="copyable-address">
                  <code>{mount.mount_path}</code>
                  <Copy value={mount.mount_path} />
                </div>
                <p className="muted-text">
                  Read-only · tls.crt and tls.key · expires {timestamp(status?.expires_at)}
                </p>
                {status?.message && <Note>{status.message}</Note>}
                {!status && <Note>The referenced certificate is unavailable.</Note>}
              </li>
            )
          })}
        </ul>
      ) : (
        <p className="muted-text">No certificate mounted into this service.</p>
      )}
    </section>
  )
}
