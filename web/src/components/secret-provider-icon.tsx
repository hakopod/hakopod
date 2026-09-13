import { Icon } from './icons'
import { ServiceIcon } from './service-icon'

export function SecretProviderIcon({ kind }: { kind: 'vault' | 'infisical' }) {
  return kind === 'infisical' ? (
    <ServiceIcon name="infisical" size={20} />
  ) : (
    <Icon name="shield" size={20} />
  )
}
