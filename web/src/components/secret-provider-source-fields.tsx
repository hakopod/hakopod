import type { Dispatch, SetStateAction } from 'react'
import type { ProviderConfiguration } from '../lib/secret-providers'
import { providerNames, providerBinding } from '../lib/secret-providers'
import { FormSection } from './form-page'
import { SecretProviderIcon } from './secret-provider-icon'
import { SelectField } from './ui/select'
import { Input } from './ui/input'

export type SourceProps = {
  configuration: ProviderConfiguration
  setConfiguration: Dispatch<SetStateAction<ProviderConfiguration>>
  readonly: boolean
}
export function SecretProviderSourceFields({
  configuration,
  setConfiguration,
  readonly,
}: SourceProps) {
  return (
    <FormSection title="Provider">
      <div className="form-grid-two">
        <label>
          Name
          <Input
            value={configuration.name}
            onChange={(event) => setConfiguration({ ...configuration, name: event.target.value })}
            required
            maxLength={40}
            readOnly={readonly}
            placeholder="production-vault"
            autoCapitalize="none"
            spellCheck={false}
          />
        </label>
        <label>
          Provider
          <SelectField
            label="Provider"
            className="secret-provider-select"
            value={configuration.kind}
            disabled={readonly}
            onValueChange={(value) =>
              setConfiguration({ ...configuration, kind: value as 'vault' | 'infisical' })
            }
            options={(['vault', 'infisical'] as const).map((kind) => ({
              value: kind,
              label: providerNames[kind],
              icon: <SecretProviderIcon kind={kind} />,
            }))}
          />
        </label>
      </div>
      <label>
        HTTPS endpoint
        <Input
          type="url"
          required
          maxLength={512}
          readOnly={readonly}
          value={configuration.endpoint}
          onChange={(event) => setConfiguration({ ...configuration, endpoint: event.target.value })}
          placeholder={
            configuration.kind === 'vault'
              ? 'https://vault.example.com'
              : 'https://us.infisical.com'
          }
          autoCapitalize="none"
          spellCheck={false}
        />
        <small className="field-help">
          Use the provider's origin without a path. HTTPS certificate verification is always
          enabled.
        </small>
      </label>
      {configuration.kind === 'vault' ? (
        <div className="form-grid-two">
          <label>
            KV v2 mount
            <Input
              required
              maxLength={512}
              readOnly={readonly}
              value={configuration.mount}
              onChange={(event) =>
                setConfiguration({ ...configuration, mount: event.target.value })
              }
              placeholder="secret"
              autoCapitalize="none"
              spellCheck={false}
            />
          </label>
          <label>
            Namespace (optional)
            <Input
              maxLength={512}
              readOnly={readonly}
              value={configuration.namespace}
              onChange={(event) =>
                setConfiguration({ ...configuration, namespace: event.target.value })
              }
              placeholder="Default namespace"
              autoCapitalize="none"
              spellCheck={false}
            />
          </label>
        </div>
      ) : (
        <div className="form-grid-two">
          <label>
            Infisical project ID
            <Input
              required
              maxLength={128}
              readOnly={readonly}
              value={configuration.project_id}
              onChange={(event) =>
                setConfiguration({ ...configuration, project_id: event.target.value })
              }
              autoCapitalize="none"
              spellCheck={false}
            />
          </label>
          <label>
            Infisical environment
            <Input
              required
              maxLength={40}
              readOnly={readonly}
              value={configuration.environment}
              onChange={(event) =>
                setConfiguration({ ...configuration, environment: event.target.value })
              }
              placeholder="prod"
              autoCapitalize="none"
              spellCheck={false}
            />
            <small className="field-help">Use the environment slug from Infisical.</small>
          </label>
        </div>
      )}
      <label>
        Root path (optional)
        <Input
          maxLength={512}
          readOnly={readonly}
          value={configuration.root_path}
          onChange={(event) =>
            setConfiguration({ ...configuration, root_path: event.target.value })
          }
          placeholder="applications"
          autoCapitalize="none"
          spellCheck={false}
        />
        <small className="field-help">
          Application references are relative to this path. Leave empty to start at the provider
          root.
        </small>
      </label>
      {readonly && (
        <p className="field-help">
          Source settings are fixed after creation. Create another provider to change the endpoint,
          root or network trust.
        </p>
      )}
      <code className="block whitespace-pre-wrap break-all text-xs">
        {providerBinding(configuration.name)}
      </code>
    </FormSection>
  )
}
