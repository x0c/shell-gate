import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { WorkspacePage } from './WorkspacePage'

function response(value: unknown, ok = true) {
  return { ok, status: ok ? 200 : 400, json: async () => value, text: async () => '' }
}

const machine = { name: 'default', target: '127.0.0.1', default: true, healthy: true, failStreak: 0, wakeable: false }
const terminal = { id: 'tab-1', title: 'shell', path: '/t/tab-1/', machine: 'default' }

function installFetch({ tabs = [terminal], createError = false, deleteError = false } = {}) {
  const calls: Array<{ path: string; options?: RequestInit }> = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, options?: RequestInit) => {
    calls.push({ path, options })
    if (path === '/api/machines') return response({ machines: [machine] })
    if (path === '/api/tabs' && (!options?.method || options.method === 'GET')) return response({ tabs })
    if (path === '/api/tabs' && options?.method === 'POST') {
      if (createError) return response({ message: 'create failed' }, false)
      return response(terminal)
    }
    if (path === '/api/presets') return response({ presets: [] })
    if (path.startsWith('/api/tabs/') && options?.method === 'DELETE') {
      return deleteError ? response({ message: 'delete failed' }, false) : response({})
    }
    if (path === '/api/netmon/ping') return response('')
    return response({})
  }))
  return calls
}

beforeEach(() => {
  sessionStorage.clear()
  localStorage.clear()
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
  sessionStorage.clear()
  localStorage.clear()
})

describe('WorkspacePage', () => {
  it('restores account tabs without creating another and never reloads them for theme changes', async () => {
    const calls = installFetch()
    const { container } = render(<WorkspacePage />)
    await waitFor(() => expect(container.querySelector('iframe.terminal-pane')).not.toBeNull())
    expect(calls.filter((call) => call.path === '/api/tabs' && call.options?.method === 'POST')).toHaveLength(0)
    const frame = container.querySelector<HTMLIFrameElement>('iframe.terminal-pane')!
    expect(frame.src).toContain('rendererType=canvas')
    const initialURL = frame.src
    await userEvent.click(screen.getByRole('button', { name: 'System' }))
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe('light'))
    expect(frame.src).toBe(initialURL)
    expect(container.querySelector('iframe.terminal-pane')).toBe(frame)
  })

  it('opens one shell only when the account has no remembered tabs', async () => {
    const calls = installFetch({ tabs: [] })
    const { container } = render(<WorkspacePage />)
    await waitFor(() => expect(container.querySelector('iframe.terminal-pane')).not.toBeNull())
    expect(calls.filter((call) => call.path === '/api/tabs' && call.options?.method === 'POST')).toHaveLength(1)
  })

  it('rejects an invalid saved terminal path without mounting or creating a replacement', async () => {
    const calls = installFetch({ tabs: [{ ...terminal, path: '' }] })
    const { container } = render(<WorkspacePage />)
    expect(await screen.findByText(/The saved terminal address is invalid/)).toBeInTheDocument()
    expect(container.querySelector('iframe.terminal-pane')).toBeNull()
    expect(calls.filter((call) => call.path === '/api/tabs' && call.options?.method === 'POST')).toHaveLength(0)
  })

  it('keeps the launch menu open when the shared trigger merges button slots', async () => {
    installFetch()
    const { container } = render(<WorkspacePage />)
    await screen.findByRole('tab', { name: 'shell' })
    await userEvent.click(container.querySelector('#btnLaunchMenu')!)
    expect(await screen.findByText('Launch', { exact: true })).toBeInTheDocument()
    expect(screen.getByText('Open an empty shell')).toBeInTheDocument()
  })

  it('keeps a tab visible when its close request fails', async () => {
    installFetch({ deleteError: true })
    const { container } = render(<WorkspacePage />)
    const close = await screen.findByRole('button', { name: 'Close shell' })
    await userEvent.click(close)
    expect(await screen.findByText('Could not close tab. Please try again.')).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'shell' })).toBeInTheDocument()
    expect(container.querySelector('iframe.terminal-pane')).not.toBeNull()
  })

  it('opens mobile menus on coarse-pointer devices without pointerdown blocking Radix triggers', async () => {
    const matchMedia = window.matchMedia
    window.matchMedia = (query: string) => ({
      matches: query === '(pointer: coarse)' || query === '(hover: none)',
      media: query,
      onchange: null,
      addListener: () => undefined,
      removeListener: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      dispatchEvent: () => false,
    })
    try {
      installFetch()
      const { container } = render(<WorkspacePage />)
      await screen.findByRole('tab', { name: 'shell' })
      await userEvent.click(container.querySelector('#btnLaunchMenu')!)
      expect(await screen.findByText('Launch', { exact: true })).toBeInTheDocument()
    } finally {
      window.matchMedia = matchMedia
    }
  })
})
