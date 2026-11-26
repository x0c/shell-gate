import { useCallback, useEffect, useLayoutEffect, useState } from 'react'
import type { TerminalTab, WorkspaceTab } from '../types'

export type ThemePreference = 'auto' | 'light' | 'dark'
export type ResolvedTheme = 'light' | 'dark'

export const THEME_KEY = 'shellgate_theme_pref'
export const THEME_LABEL: Record<ThemePreference, string> = {
  auto: 'System',
  light: 'Light',
  dark: 'Dark',
}

// Keep dark terminal colors at xterm's established defaults; only selection needs an explicit alpha.
export const TERMINAL_THEME_DARK = { selectionBackground: '#5DA5D559' }
export const TERMINAL_THEME_LIGHT = {
  background: '#FAFAF8',
  foreground: '#24292F',
  cursor: '#2F8F63',
  cursorAccent: '#FAFAF8',
  selectionBackground: '#0550AE29',
  black: '#24292F',
  red: '#C0341D',
  green: '#1A7F37',
  yellow: '#9A6700',
  blue: '#0550AE',
  magenta: '#8250DF',
  cyan: '#1B7C83',
  white: '#6E7781',
  brightBlack: '#57606A',
  brightRed: '#A40E26',
  brightGreen: '#116329',
  brightYellow: '#7A4D05',
  brightBlue: '#0A3069',
  brightMagenta: '#6639BA',
  brightCyan: '#0F5A60',
  brightWhite: '#24292F',
}

export function terminalThemeFor(resolved: ResolvedTheme) {
  return resolved === 'light' ? TERMINAL_THEME_LIGHT : TERMINAL_THEME_DARK
}

export function terminalLetterboxFor(resolved: ResolvedTheme): string {
  return resolved === 'light' ? '#FAFAF8' : '#000'
}

export function loadThemePreference(): ThemePreference {
  try {
    const value = localStorage.getItem(THEME_KEY)
    return value === 'light' || value === 'dark' || value === 'auto' ? value : 'auto'
  } catch {
    return 'auto'
  }
}

export function resolveTheme(preference: ThemePreference, light: boolean, dark: boolean): ResolvedTheme {
  if (preference !== 'auto') return preference
  if (light) return 'light'
  if (dark) return 'dark'
  return 'dark'
}

export function terminalURL(path: string, resolved: ResolvedTheme): string | null {
  if (!/^\/t\/[^/]+\/$/.test(path)) return null
  const theme = encodeURIComponent(JSON.stringify(terminalThemeFor(resolved)))
  return `${path}?rendererType=canvas&disableReconnect=true&theme=${theme}`
}

export function toWorkspaceTab(tab: TerminalTab, resolved: ResolvedTheme): WorkspaceTab | null {
  const src = terminalURL(tab.path, resolved)
  return src ? { ...tab, machine: tab.machine || '', src } : null
}

export function broadcastTheme(resolved: ResolvedTheme) {
  const message = {
    __shellgate: 'theme',
    mode: resolved,
    theme: terminalThemeFor(resolved),
    bg: terminalLetterboxFor(resolved),
  }
  for (const pane of document.querySelectorAll<HTMLIFrameElement>('.terminal-pane')) {
    try {
      pane.contentWindow?.postMessage(message, window.location.origin)
    } catch {
      // One unavailable frame must not block the other terminal tabs.
    }
  }
}

export function useWorkspaceTheme() {
  const [preference, setPreferenceState] = useState<ThemePreference>(loadThemePreference)
  const [system, setSystem] = useState(() => ({
    light: window.matchMedia?.('(prefers-color-scheme: light)').matches ?? false,
    dark: window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false,
  }))
  const resolved = resolveTheme(preference, system.light, system.dark)

  useEffect(() => {
    const light = window.matchMedia?.('(prefers-color-scheme: light)')
    const dark = window.matchMedia?.('(prefers-color-scheme: dark)')
    const update = () => setSystem({ light: light?.matches ?? false, dark: dark?.matches ?? false })
    light?.addEventListener('change', update)
    dark?.addEventListener('change', update)
    return () => {
      light?.removeEventListener('change', update)
      dark?.removeEventListener('change', update)
    }
  }, [])

  useLayoutEffect(() => {
    document.documentElement.dataset.theme = resolved
    broadcastTheme(resolved)
  }, [resolved])

  const setPreference = useCallback((next: ThemePreference) => {
    setPreferenceState(next)
    try {
      localStorage.setItem(THEME_KEY, next)
    } catch {
      // Restricted storage does not prevent the current page from changing theme.
    }
  }, [])

  const cycle = useCallback(() => {
    const order: ThemePreference[] = ['auto', 'light', 'dark']
    setPreference(order[(order.indexOf(preference) + 1) % order.length])
  }, [preference, setPreference])

  return { preference, resolved, setPreference, cycle }
}
