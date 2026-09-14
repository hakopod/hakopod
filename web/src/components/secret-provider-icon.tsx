export function SecretProviderIcon({ kind }: { kind: 'vault' | 'infisical' }) {
  return (
    <span className="secret-provider-icon" aria-hidden="true">
      {kind === 'vault' ? (
        <>
          <span className="secret-provider-mark secret-provider-mark-vault" />
          <img src="/icons/openbao.svg" alt="" width={24} height={20} />
        </>
      ) : (
        <span className="secret-provider-mark secret-provider-mark-infisical" />
      )}
    </span>
  )
}
