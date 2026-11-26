import { useEffect, useRef } from 'react'

const KEYBOARD_LOCK_SHRINK_PX = 120

export function isCoarseWorkspace(): boolean {
  try {
    return window.matchMedia('(pointer: coarse)').matches && window.matchMedia('(hover: none)').matches
  } catch {
    return false
  }
}

function broadcastKeyboard(open: boolean) {
  const message = { __shellgate: 'keyboard', open }
  for (const pane of document.querySelectorAll<HTMLIFrameElement>('.terminal-pane')) {
    try {
      pane.contentWindow?.postMessage(message, window.location.origin)
    } catch {
      // Ignore a frame that is between document loads.
    }
  }
}

export function focusActiveTerminal() {
  const pane = document.querySelector<HTMLIFrameElement>('.terminal-pane[data-active="true"]')
  if (!pane) return
  try {
    pane.focus()
    pane.contentWindow?.focus()
    pane.contentDocument?.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')?.focus({ preventScroll: true })
  } catch {
    // The iframe may still be loading.
  }
}

export function reloadMachinePanes(machine: string) {
  for (const pane of document.querySelectorAll<HTMLIFrameElement>('.terminal-pane')) {
    if (pane.dataset.machine === machine) pane.src = pane.src
  }
}

function isEditableTarget(target: Element): boolean {
  return Boolean(target.closest('input, textarea, select, [contenteditable="true"]'))
}

export function useWorkspaceViewport() {
  const keyboardHeldOpen = useRef(false)

  useEffect(() => {
    let lockWidth = 0
    let lockHeight = 0
    let baseHeight = 0

    const setKeyboard = (open: boolean) => {
      if (keyboardHeldOpen.current === open) return
      keyboardHeldOpen.current = open
      broadcastKeyboard(open)
    }

    const sync = () => {
      const viewport = window.visualViewport
      const height = Math.round(viewport?.height ?? window.innerHeight)
      const offset = Math.round(viewport?.offsetTop ?? 0)
      const width = Math.round(viewport?.width ?? window.innerWidth)

      if (isCoarseWorkspace()) {
        if (!lockWidth || Math.abs(width - lockWidth) > 8) {
          lockWidth = width
          lockHeight = 0
          baseHeight = height
          setKeyboard(false)
        }
        if (!baseHeight || height > baseHeight) baseHeight = height
        const open = baseHeight - height >= KEYBOARD_LOCK_SHRINK_PX
        if (open && (!lockHeight || height < lockHeight)) lockHeight = height
        setKeyboard(open)
        document.documentElement.style.setProperty('--app-height', `${lockHeight || height}px`)
        document.documentElement.style.setProperty('--app-offset', lockHeight ? '0px' : `${offset}px`)
        return
      }
      document.documentElement.style.setProperty('--app-height', `${height}px`)
      document.documentElement.style.setProperty('--app-offset', `${offset}px`)
    }

    const preserveWorkspaceTerminalFocus = (event: PointerEvent) => {
      if (!isCoarseWorkspace() || !(event.target instanceof Element)) return
      const target = event.target
      if (isEditableTarget(target)) return
      if (target.closest('[role="dialog"], [role="listbox"], [data-slot="select-content"]')) return
      if (target.closest('.terminal-tabs') && !target.closest('.terminal-tab')) return
      if (target.closest('.network-body')) return
      event.preventDefault()
    }

    const top = document.querySelector('.workspace-top')
    const syncTop = () => {
      if (top) document.documentElement.style.setProperty('--top-h', `${Math.round(top.getBoundingClientRect().height)}px`)
    }
    const observer = typeof ResizeObserver === 'function' && top ? new ResizeObserver(syncTop) : null
    if (top && observer) observer.observe(top)

    sync()
    syncTop()
    window.visualViewport?.addEventListener('resize', sync)
    window.visualViewport?.addEventListener('scroll', sync)
    window.addEventListener('resize', sync)
    // Run in the bubble phase so Radix triggers see !defaultPrevented before focus is held on the terminal.
    document.addEventListener('pointerdown', preserveWorkspaceTerminalFocus, false)
    return () => {
      observer?.disconnect()
      window.visualViewport?.removeEventListener('resize', sync)
      window.visualViewport?.removeEventListener('scroll', sync)
      window.removeEventListener('resize', sync)
      document.removeEventListener('pointerdown', preserveWorkspaceTerminalFocus, false)
    }
  }, [])

  return keyboardHeldOpen
}
