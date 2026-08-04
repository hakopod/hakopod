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
export function ServiceIcon({ name, size = 28 }: { name: string; size?: number }) {
  const slug = source[name]
  return (
    <span
      className={`service-brand-icon ${['valkey', 'open-webui', 'infisical', 'flowise'].includes(name) ? 'official-project-icon' : ''}`}
      style={{ width: size, height: size }}
    >
      {slug ? (
        <img
          src={`/icons/${slug}.svg`}
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
