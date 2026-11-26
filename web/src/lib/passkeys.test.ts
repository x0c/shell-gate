import { describe, expect, it } from 'vitest'
import { decodeBase64URL, encodeBase64URL, prepareRequestOptions } from './passkeys'

describe('passkey encoding', () => {
  it('round-trips base64url bytes', () => {
    const bytes = new Uint8Array([0, 1, 2, 250, 255])
    expect(Array.from(decodeBase64URL(encodeBase64URL(bytes.buffer)))).toEqual(Array.from(bytes))
  })

  it('decodes challenges and credential IDs at the browser boundary', () => {
    const challenge = encodeBase64URL(new Uint8Array([1, 2, 3]).buffer)
    const id = encodeBase64URL(new Uint8Array([4, 5]).buffer)
    const options = prepareRequestOptions({ publicKey: { challenge, allowCredentials: [{ id, type: 'public-key' }] } })
    const publicKey = options.publicKey as PublicKeyCredentialRequestOptions
    expect(publicKey.challenge).toBeInstanceOf(Uint8Array)
    expect(publicKey.allowCredentials?.[0].id).toBeInstanceOf(Uint8Array)
  })
})
