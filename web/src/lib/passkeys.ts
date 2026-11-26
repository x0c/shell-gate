interface ServerCredentialDescriptor {
  id: string
  [key: string]: unknown
}

interface ServerPasskeyOptions {
  publicKey: {
    challenge: string
    user?: { id: string; [key: string]: unknown }
    allowCredentials?: ServerCredentialDescriptor[]
    excludeCredentials?: ServerCredentialDescriptor[]
    [key: string]: unknown
  }
}

export function decodeBase64URL(value: string): Uint8Array {
  return Uint8Array.from(atob(value.replace(/-/g, '+').replace(/_/g, '/')), (char) => char.charCodeAt(0))
}

export function encodeBase64URL(value: ArrayBuffer): string {
  return btoa(String.fromCharCode(...new Uint8Array(value)))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')
}

function prepare(options: ServerPasskeyOptions): ServerPasskeyOptions {
  const publicKey = options.publicKey
  publicKey.challenge = decodeBase64URL(publicKey.challenge) as unknown as string
  if (publicKey.user) publicKey.user.id = decodeBase64URL(publicKey.user.id) as unknown as string
  for (const key of ['allowCredentials', 'excludeCredentials'] as const) {
    const descriptors = publicKey[key]
    if (descriptors) {
      publicKey[key] = descriptors.map((descriptor) => ({
        ...descriptor,
        id: decodeBase64URL(descriptor.id) as unknown as string,
      }))
    }
  }
  return options
}

export function prepareRequestOptions(options: ServerPasskeyOptions): CredentialRequestOptions {
  return prepare(options) as unknown as CredentialRequestOptions
}

export function prepareCreationOptions(options: ServerPasskeyOptions): PublicKeyCredentialCreationOptions {
  return prepare(options).publicKey as unknown as PublicKeyCredentialCreationOptions
}

export function serializeCredential(credential: PublicKeyCredential) {
  const response = credential.response as AuthenticatorAssertionResponse & AuthenticatorAttestationResponse
  const serialized: Record<string, unknown> = {
    clientDataJSON: encodeBase64URL(response.clientDataJSON),
  }
  for (const key of ['attestationObject', 'authenticatorData', 'signature', 'userHandle'] as const) {
    const value = response[key]
    if (value) serialized[key] = encodeBase64URL(value)
  }
  if (typeof response.getTransports === 'function') serialized.transports = response.getTransports()
  return {
    id: credential.id,
    rawId: encodeBase64URL(credential.rawId),
    type: credential.type,
    response: serialized,
    clientExtensionResults: credential.getClientExtensionResults(),
  }
}
