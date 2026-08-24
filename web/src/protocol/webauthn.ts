/**
 * Browser side of WebAuthn.
 *
 * The wire format is JSON with base64url strings; the browser API wants
 * ArrayBuffers. Every field that needs converting is listed explicitly rather
 * than walked generically, because a missed one fails inside the browser with
 * an error that names nothing useful.
 */

function fromBase64Url(value: string): ArrayBuffer {
  // Restore the padding and the standard alphabet that base64url drops.
  const padded = value.replace(/-/g, '+').replace(/_/g, '/')
  const binary = atob(padded + '='.repeat((4 - (padded.length % 4)) % 4))
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
  return bytes.buffer
}

function toBase64Url(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (const b of bytes) binary += String.fromCharCode(b)
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/** True when this browser can do WebAuthn at all. */
export function passkeysSupported(): boolean {
  return typeof window !== 'undefined' && !!window.PublicKeyCredential && !!navigator.credentials
}

interface ServerCreationOptions {
  publicKey: {
    challenge: string
    user: { id: string; name: string; displayName: string }
    excludeCredentials?: { id: string; type: string; transports?: string[] }[]
    [key: string]: unknown
  }
}

interface ServerRequestOptions {
  publicKey: {
    challenge: string
    allowCredentials?: { id: string; type: string; transports?: string[] }[]
    [key: string]: unknown
  }
}

/** Turns the server's registration options into what navigator.credentials wants. */
export function decodeCreationOptions(o: ServerCreationOptions): PublicKeyCredentialCreationOptions {
  const p = o.publicKey
  return {
    ...p,
    challenge: fromBase64Url(p.challenge),
    user: { ...p.user, id: fromBase64Url(p.user.id) },
    excludeCredentials: (p.excludeCredentials ?? []).map((c) => ({
      ...c,
      id: fromBase64Url(c.id),
      type: 'public-key' as const,
    })),
  } as PublicKeyCredentialCreationOptions
}

/** Turns the server's sign-in options into what navigator.credentials wants. */
export function decodeRequestOptions(o: ServerRequestOptions): PublicKeyCredentialRequestOptions {
  const p = o.publicKey
  return {
    ...p,
    challenge: fromBase64Url(p.challenge),
    allowCredentials: (p.allowCredentials ?? []).map((c) => ({
      ...c,
      id: fromBase64Url(c.id),
      type: 'public-key' as const,
    })),
  } as PublicKeyCredentialRequestOptions
}

/** Encodes a new credential for the server. */
export function encodeAttestation(credential: PublicKeyCredential) {
  const response = credential.response as AuthenticatorAttestationResponse
  return {
    id: credential.id,
    rawId: toBase64Url(credential.rawId),
    type: credential.type,
    // Present but empty rather than omitted: the server's parser expects the
    // field, and an absent one fails with a message about the wrong thing.
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: toBase64Url(response.clientDataJSON),
      attestationObject: toBase64Url(response.attestationObject),
      transports: response.getTransports?.() ?? [],
    },
  }
}

/** Encodes an assertion for the server. */
export function encodeAssertion(credential: PublicKeyCredential) {
  const response = credential.response as AuthenticatorAssertionResponse
  return {
    id: credential.id,
    rawId: toBase64Url(credential.rawId),
    type: credential.type,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      clientDataJSON: toBase64Url(response.clientDataJSON),
      authenticatorData: toBase64Url(response.authenticatorData),
      signature: toBase64Url(response.signature),
      userHandle: response.userHandle ? toBase64Url(response.userHandle) : '',
    },
  }
}
