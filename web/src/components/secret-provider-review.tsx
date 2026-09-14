import type { ProviderInput, SecretProvider } from '../lib/secret-providers'
import { providerBinding, providerNames, scopeSummary } from '../lib/secret-providers'
import { FormSection } from './form-page'
import { Note } from './shared'
import { SecretProviderIcon } from './secret-provider-icon'

// Accept public configuration only, so credential values cannot enter the review.
export function SecretProviderReview({
  configuration,
  replacingCredentials,
  current,
}: {
  configuration: Omit<ProviderInput, 'credentials'>
  replacingCredentials: boolean
  current?: SecretProvider
}) {
  const rows = [
    ['Name', configuration.name],
    ['Provider', providerNames[configuration.kind]],
    ['HTTPS endpoint', configuration.endpoint],
    ...(configuration.kind === 'vault'
      ? [
          ['KV v2 mount', configuration.mount],
          ['Namespace', configuration.namespace || 'Default'],
        ]
      : [
          ['Infisical project', configuration.project_id],
          ['Infisical environment', configuration.environment],
        ]),
    ['Root path', configuration.root_path || '/'],
    ['Private networks', configuration.private_cidrs?.join(', ') || 'None'],
    [
      'Certificate trust',
      configuration.ca_cert
        ? 'System trust and the supplied CA certificate'
        : 'System certificate trust',
    ],
    [
      'Credentials',
      replacingCredentials
        ? configuration.expected_revision === 0
          ? 'Use the entered credentials'
          : 'Replace with the entered credentials'
        : 'Keep the stored credentials',
    ],
  ]
  return (
    <div className="grid gap-4">
      <FormSection title="Review provider">
        <dl className="grid gap-3">
          {rows.map(([label, value]) => (
            <div key={label} className="grid gap-1 sm:grid-cols-[11rem_minmax(0,1fr)] sm:gap-3">
              <dt className="muted-text text-sm">{label}</dt>
              <dd className="m-0 min-w-0 break-all text-sm">
                {label === 'Provider' ? (
                  <span className="inline-flex items-center gap-2">
                    <SecretProviderIcon kind={configuration.kind} />
                    <span>{value}</span>
                  </span>
                ) : (
                  value
                )}
              </dd>
            </div>
          ))}
        </dl>
      </FormSection>
      <FormSection title="Review access">
        {current && (
          <div className="grid gap-2">
            <h3 className="text-sm font-medium">Current access · revision {current.revision}</h3>
            {current.scopes.map((scope) => (
              <p className="m-0 text-sm" key={scope.project}>
                {scopeSummary(scope)}
              </p>
            ))}
            <h3 className="mt-2 text-sm font-medium">Access after saving</h3>
          </div>
        )}
        <ul className="m-0 grid list-none gap-2 p-0">
          {configuration.scopes.map((scope) => (
            <li className="break-words text-sm" key={scope.project}>
              {scopeSummary(scope)}
            </li>
          ))}
        </ul>
        <Note>
          These projects can read values below this provider's root through deployments. Give the
          upstream identity access only to the secrets those projects should use.
        </Note>
      </FormSection>
      <FormSection title="Application reference">
        <p className="field-help">
          Add this binding under your service's secrets table in TOML, using the relative path and
          key from your provider.
        </p>
        <code className="block whitespace-pre-wrap break-all text-sm">
          {providerBinding(configuration.name || '')}
        </code>
        <p className="field-help">
          Values are read on deployment. Restart the affected service after rotating a secret to
          load its new value.
        </p>
      </FormSection>
    </div>
  )
}
