import { describe, expect, it } from 'vitest'
import { resolveTheme, terminalURL, TERMINAL_THEME_DARK, TERMINAL_THEME_LIGHT } from './theme'

describe('terminal theme and mounting', () => {
  it('forces canvas, disables ttyd reconnect and embeds the first-frame theme', () => {
    const url = terminalURL('/t/tab-1/', 'light')
    expect(url).toContain('/t/tab-1/?rendererType=canvas&disableReconnect=true&theme=')
    expect(url).not.toContain('rendererType=webgl')
    expect(decodeURIComponent(url!.split('theme=')[1])).toContain('"selectionBackground":"#0550AE29"')
  })

  it.each(['', '/', '/workspace', '/t/missing-slash', '/t/a/b/'])('rejects an invalid terminal path: %s', (path) => {
    expect(terminalURL(path, 'dark')).toBeNull()
  })

  it('keeps dark xterm defaults and uses 8-digit selection alpha in both schemes', () => {
    expect(TERMINAL_THEME_DARK).toEqual({ selectionBackground: '#5DA5D559' })
    expect(TERMINAL_THEME_LIGHT.selectionBackground).toBe('#0550AE29')
    expect(TERMINAL_THEME_DARK.selectionBackground).toHaveLength(9)
    expect(TERMINAL_THEME_LIGHT.selectionBackground).toHaveLength(9)
  })

  it('resolves system, explicit and unsupported-media themes without changing preference', () => {
    expect(resolveTheme('auto', true, false)).toBe('light')
    expect(resolveTheme('auto', false, true)).toBe('dark')
    expect(resolveTheme('auto', false, false)).toBe('dark')
    expect(resolveTheme('light', false, true)).toBe('light')
    expect(resolveTheme('dark', true, false)).toBe('dark')
  })
})
