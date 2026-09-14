import { Icon } from './icons'

const source: Record<string, string> = {
  github: 'github',
  gitlab: 'gitlab',
  google: 'google',
  postgresql: 'postgresql',
  cockroachdb: 'cockroachlabs',
  clickhouse: 'clickhouse',
  metabase: 'metabase',
  redis: 'redis',
  mysql: 'mysql',
  mariadb: 'mariadb',
  mongodb: 'mongodb',
  'uptime-kuma': 'uptimekuma',
  gitea: 'gitea',
  vllm: 'vllm',
  valkey: 'valkey',
  'open-webui': 'open-webui',
  infisical: 'infisical',
  flowise: 'flowise',
  ollama: 'ollama',
  n8n: 'n8n',
  dify: 'dify',
  langflow: 'langchain',
  qdrant: 'qdrant',
  minio: 'minio',
  docker: 'docker',
  python: 'python',
  nodejs: 'nodedotjs',
}
export function ServiceIcon({
  name,
  size = 28,
  src,
  background,
}: {
  name: string
  size?: number
  src?: string
  background?: string
}) {
  const slug = source[name]
  const catalogLogo =
    src && /^\/template-assets\/[a-z0-9.-]+\/logo\.(svg|png|webp|jpe?g)$/.test(src)
      ? src
      : undefined
  return (
    <span
      className={`service-brand-icon ${catalogLogo ? `catalog-brand-icon ${background === 'dark' ? 'catalog-brand-dark' : ''}` : ''} ${['valkey', 'open-webui', 'infisical', 'flowise'].includes(name) ? 'official-project-icon' : ''}`}
      style={{ width: size, height: size }}
    >
      {catalogLogo || slug ? (
        <img
          src={catalogLogo || `/icons/${slug}.svg`}
          alt=""
          width={size}
          height={size}
          loading="lazy"
          decoding="async"
        />
      ) : (
        <Icon
          name={name === 'valkey' ? 'database' : name.includes('agent') ? 'code' : 'box'}
          size={size}
        />
      )}
    </span>
  )
}
