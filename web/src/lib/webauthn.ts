// Native WebAuthn only: no extra client library or persistent credential cache.
const decode = (value: string) =>
  Uint8Array.from(atob(value.replace(/-/g, '+').replace(/_/g, '/')), (char) => char.charCodeAt(0))
    .buffer
const encode = (value: ArrayBuffer) =>
  btoa(String.fromCharCode(...new Uint8Array(value)))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')

export async function passkeyCredential(
  options: Record<string, unknown>,
  register = false,
): Promise<Record<string, unknown>> {
  if (!window.isSecureContext || !window.PublicKeyCredential)
    throw new Error('Passkeys require a supported browser on HTTPS or localhost.')
  const parsed = structuredClone(options) as Record<string, any>
  parsed.challenge = decode(parsed.challenge)
  if (parsed.user?.id) parsed.user.id = decode(parsed.user.id)
  for (const key of ['allowCredentials', 'excludeCredentials']) {
    if (Array.isArray(parsed[key]))
      parsed[key] = parsed[key].map((item: Record<string, string>) => ({
        ...item,
        id: decode(item.id),
      }))
  }
  const credential = (
    register
      ? await navigator.credentials.create({
          publicKey: parsed as PublicKeyCredentialCreationOptions,
        })
      : await navigator.credentials.get({ publicKey: parsed as PublicKeyCredentialRequestOptions })
  ) as PublicKeyCredential | null
  if (!credential) throw new Error('No passkey was selected.')
  const response = credential.response
  const serialized: Record<string, unknown> = { clientDataJSON: encode(response.clientDataJSON) }
  if (response instanceof AuthenticatorAttestationResponse) {
    serialized.attestationObject = encode(response.attestationObject)
    serialized.transports = response.getTransports?.() || []
  } else {
    const assertion = response as AuthenticatorAssertionResponse
    serialized.authenticatorData = encode(assertion.authenticatorData)
    serialized.signature = encode(assertion.signature)
    serialized.userHandle = assertion.userHandle ? encode(assertion.userHandle) : null
  }
  return {
    id: credential.id,
    rawId: encode(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: serialized,
  }
}
