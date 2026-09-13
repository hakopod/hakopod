import type { Dispatch, SetStateAction } from 'react'
import type { ProviderCredentials } from '../lib/secret-providers'
import type { SourceProps } from './secret-provider-source-fields'
import { FormSection } from './form-page'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'

export function SecretProviderCredentialFields({
  configuration,
  readonly,
  credentials,
  setCredentials,
  replaceCredentials,
  setReplaceCredentials,
}: Pick<SourceProps, 'configuration' | 'readonly'> & {
  credentials: ProviderCredentials
  setCredentials: Dispatch<SetStateAction<ProviderCredentials>>
  replaceCredentials: boolean
  setReplaceCredentials: (value: boolean) => void
}) {
  return (
    <FormSection title="Credentials">
      {readonly && (
        <label className="checkbox-row">
          <Input
            type="checkbox"
            checked={replaceCredentials}
            onChange={(event) => setReplaceCredentials(event.target.checked)}
          />
          Replace stored credentials
        </label>
      )}
      <p className="field-help">
        {readonly && !replaceCredentials
          ? 'The stored credentials will be kept. Saved credential values are never returned.'
          : configuration.kind === 'vault'
            ? 'Use a token with read access only to the intended KV v2 subtree.'
            : 'Use a machine identity with read access only to the intended project, environment and folders.'}
      </p>
      {replaceCredentials &&
        (configuration.kind === 'vault' ? (
          <label>
            Vault token
            <Input
              type="password"
              required
              maxLength={8192}
              value={credentials.token}
              onChange={(event) => setCredentials({ ...credentials, token: event.target.value })}
              autoComplete="new-password"
              spellCheck={false}
            />
          </label>
        ) : (
          <div className="form-grid-two">
            <label>
              Client ID
              <Input
                type="password"
                required
                maxLength={8192}
                value={credentials.client_id}
                onChange={(event) =>
                  setCredentials({ ...credentials, client_id: event.target.value })
                }
                autoComplete="new-password"
                spellCheck={false}
              />
            </label>
            <label>
              Client secret
              <Input
                type="password"
                required
                maxLength={8192}
                value={credentials.client_secret}
                onChange={(event) =>
                  setCredentials({ ...credentials, client_secret: event.target.value })
                }
                autoComplete="new-password"
                spellCheck={false}
              />
            </label>
          </div>
        ))}
    </FormSection>
  )
}

export function SecretProviderNetworkFields({
  configuration,
  setConfiguration,
  readonly,
  privateNetworks,
  setPrivateNetworks,
}: SourceProps & { privateNetworks: string; setPrivateNetworks: (value: string) => void }) {
  return (
    <FormSection
      title="Network trust"
      description="Allow only the private networks and certificate authorities needed by this provider."
    >
      <label>
        Private network CIDRs (optional)
        <Textarea
          rows={2}
          readOnly={readonly}
          maxLength={1024}
          value={privateNetworks}
          onChange={(event) => setPrivateNetworks(event.target.value)}
          placeholder="10.20.0.0/24"
          spellCheck={false}
        />
        <small className="field-help">
          Use one private CIDR per line. Public endpoints need no entry. Loopback, link-local and
          cloud metadata addresses are always blocked.
        </small>
      </label>
      <details open={Boolean(configuration.ca_cert)}>
        <summary className="cursor-pointer text-sm">
          Custom certificate authority (optional)
        </summary>
        <label className="mt-3 grid gap-2">
          CA certificate in PEM format
          <Textarea
            rows={4}
            maxLength={32768}
            readOnly={readonly}
            value={configuration.ca_cert}
            onChange={(event) =>
              setConfiguration({ ...configuration, ca_cert: event.target.value })
            }
            placeholder="-----BEGIN CERTIFICATE-----"
            spellCheck={false}
          />
          <small className="field-help">
            Add your provider's public CA certificate. Do not paste a private key.
          </small>
        </label>
      </details>
    </FormSection>
  )
}
