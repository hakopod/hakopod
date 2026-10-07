import type { DatabaseSpec } from '../lib/databases'
import {
  databaseBindingDatabaseLabel,
  databaseBindingSSLOptions,
  managedDatabaseUser,
  managedDatabaseName,
  type DatabaseBindingDraft,
  type DatabaseBindingOptions,
} from '../lib/database-binding'
import { secretReference } from '../lib/secret-reference'
import { FormSection } from './form-page'
import { Note } from './shared'
import { Input } from './ui/input'
import { SelectField } from './ui/select'

export function DatabaseBindingFields({ spec, endpoint, draft, onChange, disabled, applicationName, secretNames, secretsLoading, secretsFailed, canSavePassword }: {
  spec: DatabaseSpec
  endpoint?: string
  draft: DatabaseBindingDraft
  onChange: (draft: DatabaseBindingDraft) => void
  disabled: boolean
  applicationName?: string
  secretNames: string[]
  secretsLoading: boolean
  secretsFailed: boolean
  canSavePassword: boolean
}) {
  const sslOptions = databaseBindingSSLOptions(spec, endpoint)
  const managedOnly = spec.engine === 'duckdb'
  const update = (change: Partial<DatabaseBindingDraft>) => onChange({ ...draft, ...change })
  const selectedMissing = Boolean(draft.passwordRef && !secretNames.includes(draft.passwordRef))
  return <FormSection title="Connection options" description="Blank user and database fields use managed defaults. These choices connect to an existing database and user; they do not create either.">
    <div className="grid min-w-0 gap-4 sm:grid-cols-2">
      <label className="min-w-0" htmlFor="binding-username">Username
        <Input id="binding-username" aria-label="Username" value={draft.username} maxLength={64} disabled={disabled || managedOnly} autoComplete="off" spellCheck={false} placeholder={`Managed default (${managedDatabaseUser(spec.engine, endpoint)})`} aria-describedby="binding-user-help" onChange={(event) => update({ username: event.target.value })} />
        <span id="binding-user-help" className="field-help">{managedOnly ? `MyDuck uses the managed ${managedDatabaseUser(spec.engine, endpoint)} account for this protocol.` : spec.engine === 'vitess' ? 'This managed Vitess instance supports the app login.' : 'Use an existing user. A different username requires its password.'}</span>
      </label>
      <label className="min-w-0" htmlFor="binding-database">{databaseBindingDatabaseLabel(spec.engine)}
        {spec.engine === 'redis' ? <SelectField id="binding-database" label="Database index" value={draft.database} disabled={disabled} onValueChange={(database) => update({ database })} options={[
          { value: '', label: 'Managed default (0)' },
          ...Array.from({ length: spec.mode === 'cluster' ? 1 : 16 }, (_, index) => ({ value: String(index), label: String(index) })),
        ]} aria-describedby="binding-database-help" /> : <Input id="binding-database" aria-label={databaseBindingDatabaseLabel(spec.engine)} value={draft.database} maxLength={64} disabled={disabled || managedOnly} autoComplete="off" spellCheck={false} placeholder={`Managed default (${managedDatabaseName(spec)})`} aria-describedby="binding-database-help" onChange={(event) => update({ database: event.target.value })} />}
        <span id="binding-database-help" className="field-help">{spec.engine === 'redis' ? spec.mode === 'cluster' ? 'Redis Cluster supports only database 0.' : 'Choose an existing logical database from 0 to 15.' : spec.engine === 'vitess' ? 'This instance uses the app keyspace. The endpoint selects primary or replica routing.' : spec.engine === 'oracle' ? 'Use an existing Oracle service name.' : 'Use an existing database that this user can access.'}</span>
      </label>
      <label className="min-w-0" htmlFor="binding-password-source">Password
        <SelectField id="binding-password-source" label="Password source" value={draft.passwordSource} disabled={disabled || !applicationName || managedOnly} onValueChange={(value) => update({ passwordSource: value as DatabaseBindingDraft['passwordSource'] })} options={[
          { value: 'managed', label: 'Managed password' },
          { value: 'existing', label: 'Saved application secret' },
          { value: 'enter', label: 'Enter password', disabled: !canSavePassword },
        ]} aria-describedby="binding-password-help" />
        <span id="binding-password-help" className="field-help">{managedOnly ? 'MyDuck currently uses its managed app password.' : applicationName ? `Saved passwords belong to ${applicationName}.` : 'Choose an application to set its password.'}</span>
      </label>
      <label className="min-w-0" htmlFor="binding-ssl">SSL mode
        <SelectField id="binding-ssl" label="SSL mode" value={draft.sslMode} disabled={disabled || sslOptions.length === 1} onValueChange={(sslMode) => update({ sslMode: sslMode as DatabaseBindingDraft['sslMode'] })} options={sslOptions} aria-describedby="binding-ssl-help" />
        <span id="binding-ssl-help" className="field-help">{spec.engine === 'duckdb' ? 'TLS is required. Configure the selected MySQL or PostgreSQL driver to trust the mounted database CA.' : spec.engine === 'mysql' || spec.engine === 'vitess' ? 'Configure TLS and the mounted CA in your MySQL driver.' : spec.tls?.mode === 'required' ? 'TLS is required. Configure your client to trust the mounted database CA and verify the server hostname.' : 'This legacy database does not provide TLS.'}</span>
      </label>
      {draft.passwordSource === 'existing' && <label className="min-w-0 sm:col-span-2" htmlFor="binding-secret">Saved password
        <SelectField id="binding-secret" label="Saved password" required value={draft.passwordRef} disabled={disabled || secretsLoading || secretsFailed} onValueChange={(passwordRef) => update({ passwordRef })} options={[
          { value: '', label: secretsLoading ? 'Loading application secrets…' : 'Choose a saved secret' },
          ...secretNames.map((name) => ({ value: name, label: name })),
          ...(selectedMissing ? [{ value: draft.passwordRef, label: `${draft.passwordRef} (unavailable)`, disabled: true }] : []),
        ]} aria-describedby="binding-secret-help" />
        <span id="binding-secret-help" className="field-help">{secretsFailed ? 'Application secrets could not be loaded. Retry before selecting a saved password.' : !secretsLoading && !secretNames.length ? 'No passwords are saved for this application. Choose Enter password to save one.' : 'Only the secret name appears in the connection configuration.'}</span>
      </label>}
      {draft.passwordSource === 'enter' && <label className="min-w-0 sm:col-span-2" htmlFor="binding-password">Existing password
        <Input id="binding-password" aria-label="Existing password" type="password" required value={draft.passwordValue} disabled={disabled || !canSavePassword} maxLength={4096} autoComplete="new-password" aria-describedby="binding-password-save-help" onChange={(event) => update({ passwordValue: event.target.value })} />
        <span id="binding-password-save-help" className="field-help">Up to 4096 bytes. Review connection saves this value as a new application secret and shows only its reference. Saving does not change the database user’s password.</span>
      </label>}
    </div>
    {draft.sslMode === 'require' && <Note>This mode encrypts traffic without verifying the server certificate. Use verify-full when your client supports it.</Note>}
    {draft.sslMode === 'verify-ca' && <Note>This mode verifies the certificate authority but does not require hostname verification. Use verify-full when possible.</Note>}
    {draft.sslMode === 'disable' && <Note>This connection sends traffic without TLS encryption.</Note>}
    {spec.engine === 'mongodb' && <p className="field-help">A custom user authenticates against the selected database. The managed user authenticates against its managed database.</p>}
  </FormSection>
}

export function databaseBindingSummary(binding: DatabaseBindingOptions) {
  return [
    ['User', binding.username || 'Managed default'],
    ['Database', binding.database || 'Managed default'],
    ['Password', binding.password ? `Secret: ${secretReference(binding.password)}` : 'Managed password'],
    ['SSL', binding.ssl_mode || 'Database policy'],
  ] as const
}

export function DatabaseBindingSummary({ binding }: { binding: DatabaseBindingOptions }) {
  return <dl className="grid min-w-0 gap-3 text-sm sm:grid-cols-2" aria-label="Connection options">
    {databaseBindingSummary(binding).map(([label, value]) => <div className="min-w-0" key={label}><dt className="text-muted-foreground">{label}</dt><dd className="break-all">{value}</dd></div>)}
  </dl>
}
