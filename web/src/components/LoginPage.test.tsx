import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { loadExternalAuth } from '../lib/external-auth'
import { assignLocation, replaceHistory, replaceLocation } from '../lib/navigation'
import { LoginPage } from './LoginPage'

vi.mock('../lib/navigation', () => ({
  assignLocation: vi.fn(),
  replaceLocation: vi.fn(),
  replaceHistory: vi.fn(),
}))
vi.mock('../lib/external-auth', () => ({ loadExternalAuth: vi.fn() }))

type FetchHandler = (path: string, options?: RequestInit) => Promise<unknown> | unknown

function response(value: unknown, ok = true) {
  return { ok, status: ok ? 200 : 400, json: async () => value }
}

function installFetch(handler: FetchHandler) {
  const fetcher = vi.fn(async (path: string, options?: RequestInit) => await handler(path, options))
  vi.stubGlobal('fetch', fetcher)
  return fetcher
}

function routes(config: unknown, pending = false, other?: FetchHandler) {
  return (path: string, options?: RequestInit) => {
    if (path === '/api/config') return response(config)
    if (path === '/api/totp/pending') return response({ required: pending })
    if (other) return other(path, options)
    return response({})
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

beforeEach(() => {
  window.history.replaceState({}, '', '/login')
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true })
  Object.defineProperty(window, 'PublicKeyCredential', { configurable: true, value: class {} })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

describe('LoginPage', () => {
  it('loads SSO without flashing passkeys, redirects once and restores on pageshow', async () => {
    const config = deferred<unknown>()
    installFetch((path) => path === '/api/config' ? config.promise : response({ required: false }))
    render(<LoginPage />)
    expect(screen.getByText('Loading sign-in options…')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /passkey/i })).not.toBeInTheDocument()
    config.resolve(response({ authMode: 'sso', ssoIssuer: 'https://sso.example.com/realm' }))
    const button = await screen.findByRole('button', { name: 'Sign in with SSO' })
    expect(screen.getByText('sso.example.com')).toBeInTheDocument()
    expect(screen.queryByLabelText('Password')).not.toBeInTheDocument()
    await userEvent.click(button)
    expect(assignLocation).toHaveBeenCalledWith('/api/sso/login')
    expect(button).toHaveAttribute('aria-busy', 'true')
    window.dispatchEvent(new Event('pageshow'))
    expect(await screen.findByRole('button', { name: 'Sign in with SSO' })).toBeEnabled()
  })

  it('stops loading on configuration failure and retries', async () => {
    let attempts = 0
    installFetch((path) => {
      if (path === '/api/config') {
        attempts += 1
        if (attempts === 1) throw new Error('network')
        return response({ authMode: 'sso' })
      }
      return response({ required: false })
    })
    render(<LoginPage />)
    const retry = await screen.findByRole('button', { name: 'Retry' })
    expect(screen.getByText(/Check your connection and retry/)).toBeInTheDocument()
    await userEvent.click(retry)
    expect(await screen.findByRole('button', { name: 'Sign in with SSO' })).toBeEnabled()
    expect(attempts).toBe(2)
  })

  it('uses safe callback error copy and removes OAuth parameters', async () => {
    window.history.replaceState({}, '', '/login?error=unverified_email&code=sensitive')
    installFetch(routes({ authMode: 'sso' }))
    render(<LoginPage />)
    expect(await screen.findByText(/Contact your administrator/)).toBeInTheDocument()
    expect(replaceHistory).toHaveBeenCalledWith('/login')
    expect(await screen.findByRole('button', { name: 'Sign in with SSO' })).toBeEnabled()
  })

  it('retains local password, setup and passkey routes', async () => {
    installFetch(routes({ authMode: 'local' }))
    render(<LoginPage />)
    expect(await screen.findByLabelText('Email', { selector: '#loginEmail' })).toBeInTheDocument()
    expect(screen.getByLabelText('Password')).toBeInTheDocument()
    expect(screen.getByText('Set up or recover an account')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Sign in with a passkey/ })).toBeEnabled()
    expect(screen.queryByText('Sign in with SSO')).not.toBeInTheDocument()
  })

  it('keeps optional SSO independent from the local passkey route', async () => {
    const buffer = new ArrayBuffer(2)
    const credential = {
      id: 'credential',
      rawId: buffer,
      type: 'public-key',
      response: {
        clientDataJSON: buffer,
        authenticatorData: buffer,
        signature: buffer,
        userHandle: buffer,
        getTransports: () => ['internal'],
      },
      getClientExtensionResults: () => ({}),
    }
    const get = vi.fn().mockResolvedValue(credential)
    Object.defineProperty(navigator, 'credentials', { configurable: true, value: { get } })
    const fetcher = installFetch(routes({ authMode: 'local', ssoEnabled: true }, false, (path) => {
      if (path === '/api/passkeys/login/begin') return response({ publicKey: { challenge: 'AQID' } })
      if (path === '/api/passkeys/login/finish') return response({ status: 'ok' })
      return response({})
    }))
    render(<LoginPage />)
    const passkey = await screen.findByRole('button', { name: /Sign in with a passkey/ })
    const sso = screen.getByRole('button', { name: 'Sign in with SSO' })
    expect(passkey).toBeEnabled()
    expect(sso).toBeEnabled()
    await userEvent.click(passkey)
    await waitFor(() => expect(get).toHaveBeenCalledTimes(1))
    expect(fetcher).toHaveBeenCalledWith('/api/passkeys/login/finish', expect.objectContaining({ method: 'POST' }))
    expect(replaceLocation).toHaveBeenCalledWith('/')
    await userEvent.click(sso)
    expect(assignLocation).toHaveBeenCalledWith('/api/sso/login')
  })

  it('retains the Supabase passkey exchange alongside optional SSO', async () => {
    const signInWithPasskey = vi.fn().mockResolvedValue({ data: { session: { access_token: 'fixture-token' } } })
    vi.mocked(loadExternalAuth).mockResolvedValue({ createClient: () => ({ auth: { signInWithPasskey } }) })
    const fetcher = installFetch(routes({ authMode: 'supabase', ssoEnabled: true, supabaseUrl: 'https://auth.example.com', anonKey: 'anon' }))
    render(<LoginPage />)
    const passkey = await screen.findByRole('button', { name: /Sign in with a passkey/ })
    expect(screen.getByRole('button', { name: 'Sign in with SSO' })).toBeEnabled()
    await userEvent.click(passkey)
    await waitFor(() => expect(signInWithPasskey).toHaveBeenCalledTimes(1))
    expect(fetcher).toHaveBeenCalledWith('/api/session', expect.objectContaining({ body: JSON.stringify({ access_token: 'fixture-token' }) }))
    expect(replaceLocation).toHaveBeenCalledWith('/')
  })

  it('hides primary routes for pending 2FA and accepts a recovery code', async () => {
    const fetcher = installFetch(routes({ authMode: 'local' }, true, (path) => {
      if (path === '/api/totp/verify') return response({ status: 'ok' })
      return response({})
    }))
    render(<LoginPage />)
    expect(await screen.findByText('Two-factor authentication')).toBeInTheDocument()
    expect(screen.queryByLabelText('Password')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Use a recovery code' }))
    const input = screen.getByLabelText('Recovery code')
    await userEvent.type(input, 'test recovery code')
    await userEvent.click(screen.getByRole('button', { name: 'Verify' }))
    await waitFor(() => expect(replaceLocation).toHaveBeenCalledWith('/'))
    expect(fetcher).toHaveBeenCalledWith('/api/totp/verify', expect.objectContaining({ body: JSON.stringify({ code: 'test recovery code' }) }))
    expect(input).toHaveValue('')
  })

  it('keeps an incorrect second factor on verification with an actionable error', async () => {
    installFetch(routes({ authMode: 'sso' }, true, (path) => {
      if (path === '/api/totp/verify') return response({ message: 'Code already used. Try a new code.' }, false)
      return response({})
    }))
    render(<LoginPage />)
    const input = await screen.findByLabelText('Authenticator code')
    await userEvent.type(input, '123456')
    await userEvent.click(screen.getByRole('button', { name: 'Verify' }))
    expect(await screen.findByText('Code already used. Try a new code.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Verify' })).toBeEnabled()
    expect(replaceLocation).not.toHaveBeenCalled()
  })
})
