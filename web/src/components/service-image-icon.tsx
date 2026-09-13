import { ServiceIcon } from './service-icon'

const imageNames: Record<string, string> = {
  postgres: 'postgresql',
  postgresql: 'postgresql',
  cockroach: 'cockroachdb',
  'clickhouse-server': 'clickhouse',
  metabase: 'metabase',
  redis: 'redis',
  mysql: 'mysql',
  mariadb: 'mariadb',
  mongo: 'mongodb',
  valkey: 'valkey',
  minio: 'minio',
  qdrant: 'qdrant',
  n8n: 'n8n',
  ollama: 'ollama',
  infisical: 'infisical',
  'open-webui': 'open-webui',
  'uptime-kuma': 'uptime-kuma',
  node: 'nodejs',
  python: 'python',
}

export function ServiceImageIcon({ image, size = 22 }: { image: string; size?: number }) {
  const name = image.split('/').at(-1)?.split(/[:@]/)[0] || ''
  return <ServiceIcon name={imageNames[name] || 'docker'} size={size} />
}
