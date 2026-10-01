import type { DatabasePublicEndpointCapabilities, DatabasePublicEndpointRoute } from './databases'

const purposeLabels: Record<string, string> = {
  read_write: 'Direct read and write',
  read_only: 'Direct read only',
  pooled_read_write: 'Pooled read and write',
  pooled_read_only: 'Pooled read only',
  native: 'Native protocol',
  https: 'HTTPS',
  cluster: 'Redis cluster',
}

const protocolLabels: Record<string, string> = {
  postgresql: 'PostgreSQL',
  mysql: 'MySQL',
  mongodb: 'MongoDB',
  redis: 'Redis',
  clickhouse_native: 'ClickHouse native',
  oracle_tcps: 'Oracle TCPS',
  https: 'HTTPS',
}

const routingLabels: Record<DatabasePublicEndpointRoute['routing'], string> = {
  direct: 'Direct database route',
  pgbouncer: 'PgBouncer pool',
  mysql_router: 'MySQL Router',
  vitess_gateway: 'Vitess gateway',
  replica_set_horizons: 'MongoDB member discovery',
  cluster_discovery: 'Redis cluster discovery',
  client_address_mapping: 'Redis client address mapping required',
}

export function databasePublicEndpointRouteLabel(route: DatabasePublicEndpointRoute) {
  return purposeLabels[route.purpose] || route.purpose.replace(/_/g, ' ')
}

export function databasePublicEndpointProtocolLabel(route: DatabasePublicEndpointRoute) {
  return protocolLabels[route.protocol] || route.protocol.replace(/_/g, ' ')
}

export function databasePublicEndpointRoutingLabel(route: DatabasePublicEndpointRoute) {
  return routingLabels[route.routing]
}

export function databasePublicEndpointAccessLabel(route: DatabasePublicEndpointRoute) {
  return route.read_only ? 'Read only' : 'Read and write'
}

export function databasePublicEndpointRouteOptions(routes: DatabasePublicEndpointRoute[]) {
  return routes.map((route) => ({
    value: route.purpose,
    label: `${databasePublicEndpointRouteLabel(route)} · ${databasePublicEndpointProtocolLabel(route)} via ${databasePublicEndpointRoutingLabel(route)}`,
  }))
}

export function databasePublicEndpointRouteForPurpose(
  routes: DatabasePublicEndpointRoute[],
  purpose?: string,
) {
  return routes.find((route) => route.purpose === purpose)
}

export function parseDatabasePublicEndpointCIDRs(value: string) {
  return value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean)
}

export function databasePublicEndpointOperationTerminal(status?: string) {
  return status === 'succeeded' || status === 'failed' || status === 'cancelled'
}

export function databasePublicEndpointPublicationAvailable(
  capabilities?: DatabasePublicEndpointCapabilities,
  failed = false,
) {
  return Boolean(!failed && capabilities?.available && capabilities.routes.length)
}

export function databasePublicEndpointPublicationUnavailableReason(
  capabilities: DatabasePublicEndpointCapabilities | undefined,
  pending: boolean,
  failed: boolean,
) {
  if (pending) return 'Checking whether new public endpoints can be published.'
  if (failed || !capabilities)
    return 'Endpoint publication availability could not be checked. New endpoint publication is disabled.'
  if (!capabilities.available)
    return (
      capabilities.unavailable_reason || 'New public endpoints are not available for this database.'
    )
  if (!capabilities.routes.length)
    return 'No public endpoint routes are available for this database.'
  return ''
}

export function databasePublicEndpointRevocationDisabled(
  busy: boolean,
  operationBlocksMutations: boolean,
  reviewStale: boolean,
) {
  return busy || operationBlocksMutations || reviewStale
}
