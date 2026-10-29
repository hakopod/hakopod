// WireGuard keys are raw X25519 keys in base64. The browser generates the
// device's key pair so its private key never leaves this page.
const base64 = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes))

export async function wireguardKeyPair() {
  const pair = (await crypto.subtle.generateKey({ name: 'X25519' }, true, [
    'deriveBits',
  ])) as CryptoKeyPair
  const publicKey = new Uint8Array(await crypto.subtle.exportKey('raw', pair.publicKey))
  // PKCS #8 for X25519 ends with the 32-byte private scalar.
  const pkcs8 = new Uint8Array(await crypto.subtle.exportKey('pkcs8', pair.privateKey))
  return { publicKey: base64(publicKey), privateKey: base64(pkcs8.slice(-32)) }
}

export function wireguardProfile(profile: {
  privateKey: string
  address: string
  serverPublicKey: string
  endpoint: string
  network: string
}) {
  return [
    '[Interface]',
    `PrivateKey = ${profile.privateKey}`,
    `Address = ${profile.address}/32`,
    '',
    '[Peer]',
    `PublicKey = ${profile.serverPublicKey}`,
    `Endpoint = ${profile.endpoint}`,
    `AllowedIPs = ${profile.network}`,
    'PersistentKeepalive = 25',
    '',
  ].join('\n')
}
