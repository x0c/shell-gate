import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useState } from 'react'
import { AccountSecurityDialog } from './AccountSecurityDialog'

function response(value: unknown, ok = true) {
  return { ok, status: ok ? 200 : 400, json: async () => value }
}

function installFetch(handler: (path: string, options?: RequestInit) => unknown) {
  const calls: Array<{ path: string; body?: unknown }> = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, options?: RequestInit) => {
    calls.push({ path, body: options?.body ? JSON.parse(String(options.body)) : undefined })
    return handler(path, options)
  }))
  return calls
}

function Harness({ initial = true }: { initial?: boolean }) {
  const [open, setOpen] = useState(initial)
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>Open security</button>
      <AccountSecurityDialog open={open} onOpenChange={setOpen} />
    </>
  )
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

describe('AccountSecurityDialog', () => {
  it('opens explicitly, confirms enrollment and clears recovery material on close', async () => {
    const calls = installFetch((path) => {
      if (path === '/api/totp') return response({ email: 'user@example.com', enabled: false, recoveryCodesRemaining: 0 })
      if (path === '/api/totp/setup') return response({ setupId: 'setup', secret: 'TEST-SEED', qr: 'data:image/png;base64,test' })
      return response({ enabled: true, recoveryCodes: ['ONE-TIME-RECOVERY'] })
    })
    render(<Harness initial={false} />)
    expect(calls).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Open security' }))
    expect(await screen.findByRole('button', { name: 'Set up authenticator' })).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Set up authenticator' }))
    expect(await screen.findByText('TEST-SEED')).toBeInTheDocument()
    expect(calls.some((call) => call.path === '/api/totp/enable')).toBe(false)
    await userEvent.type(screen.getByLabelText('Authenticator code'), '123456')
    await userEvent.click(screen.getByRole('button', { name: 'Enable 2FA' }))
    expect(await screen.findByText('ONE-TIME-RECOVERY')).toBeInTheDocument()
    expect(calls.at(-1)).toEqual({ path: '/api/totp/enable', body: { setupId: 'setup', code: '123456' } })
    expect(screen.queryByText('TEST-SEED')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.queryByText('ONE-TIME-RECOVERY')).not.toBeInTheDocument()
  })

  it('does not repopulate a late setup response after close and reopen', async () => {
    const setup = deferred<unknown>()
    installFetch((path) => {
      if (path === '/api/totp') return response({ enabled: false, email: 'user@example.com', recoveryCodesRemaining: 0 })
      return setup.promise
    })
    render(<Harness />)
    await userEvent.click(await screen.findByRole('button', { name: 'Set up authenticator' }))
    await userEvent.click(screen.getByRole('button', { name: 'Close' }))
    setup.resolve({ setupId: 'late', secret: 'TEST-LATE-SEED', qr: 'late' })
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    await userEvent.click(screen.getByRole('button', { name: 'Open security' }))
    expect(await screen.findByRole('button', { name: 'Set up authenticator' })).toBeInTheDocument()
    expect(screen.queryByText('TEST-LATE-SEED')).not.toBeInTheDocument()
  })

  it('offers retry after a status failure and sign-in after recent-auth failure', async () => {
    let attempts = 0
    let reauthenticate = false
    installFetch((path) => {
      if (path === '/api/totp') {
        attempts += 1
        if (attempts === 1) return response({ message: 'Temporarily unavailable' }, false)
        return response({ enabled: false, email: 'user@example.com', recoveryCodesRemaining: 0 })
      }
      if (reauthenticate) return response({ code: 'reauth_required', message: 'Sign in again' }, false)
      return response({})
    })
    render(<Harness />)
    await userEvent.click(await screen.findByRole('button', { name: 'Retry' }))
    reauthenticate = true
    await userEvent.click(await screen.findByRole('button', { name: 'Set up authenticator' }))
    expect(await screen.findByRole('link', { name: 'Sign in again' })).toHaveAttribute('href', '/logout')
  })

  it('does not turn an invalidated session into a retry loop', async () => {
    installFetch(() => response({ code: 'signin_required', message: 'Sign-in required' }, false))
    render(<Harness />)
    expect(await screen.findByRole('link', { name: 'Sign in again' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument()
  })
})
