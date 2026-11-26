import { afterEach, describe, expect, it, vi } from 'vitest'
import { chooseEdge } from './edge'

afterEach(() => vi.unstubAllGlobals())

describe('edge selection', () => {
  it('keeps a reachable preferred edge', async () => {
    const fetcher = vi.fn().mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetcher)
    const host = await chooseEdge([{ host: 'a.example.com' }, { host: 'b.example.com' }], 'b.example.com')
    expect(host).toBe('b.example.com')
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('selects the lowest measured RTT when no preference is usable', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true }))
    const now = vi.spyOn(performance, 'now')
      .mockReturnValueOnce(0).mockReturnValueOnce(30)
      .mockReturnValueOnce(0).mockReturnValueOnce(10)
    const host = await chooseEdge([{ host: 'a.example.com' }, { host: 'b.example.com' }], null)
    expect(host).toBe('b.example.com')
    expect(now).toHaveBeenCalledTimes(4)
    now.mockRestore()
  })

  it('fails closed when every probe fails', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
    expect(await chooseEdge([{ host: 'a.example.com' }], null)).toBeNull()
  })
})
