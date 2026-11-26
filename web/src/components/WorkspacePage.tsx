import {
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  Notice,
  Skeleton,
  Toaster,
  toast,
} from '@x0c/ui'
import {
  Activity,
  Check,
  ListPlus,
  Menu,
  MoreHorizontal,
  Plus,
  ShieldCheck,
  SunMoon,
  Terminal,
  Wrench,
  X,
} from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { errorMessage, postBody, workspaceAPI } from '../lib/api'
import { useNetworkMonitor } from '../lib/netmon'
import { THEME_LABEL, toWorkspaceTab, useWorkspaceTheme } from '../lib/theme'
import { useLiveState } from '../lib/useLiveState'
import { focusActiveTerminal, isCoarseWorkspace, reloadMachinePanes, useWorkspaceViewport } from '../lib/viewport'
import { lastTabOnMachine, rememberedTabOnMachine, tabAfterClose, tabsOnMachine, validActiveByMachine } from '../lib/workspace-state'
import type { Machine, Preset, TerminalTab, WorkspaceTab } from '../types'
import { AccountSecurityDialog } from './AccountSecurityDialog'
import { NetworkPanel } from './NetworkPanel'
import { PresetDialog } from './PresetDialog'

const TAB_STATE_KEY = 'shellgate_tabs_state'
const MACHINE_KEY = 'shellgate_machine'

interface OpenTabArgs {
  presetId?: string
  title?: string
  command?: string
}

function savedTabState(): { activeId: string | null; activeByMachine: unknown } | null {
  try {
    const value = JSON.parse(sessionStorage.getItem(TAB_STATE_KEY) || 'null') as { activeId?: string; activeByMachine?: unknown } | null
    return value && typeof value === 'object' ? { activeId: value.activeId || null, activeByMachine: value.activeByMachine } : null
  } catch {
    return null
  }
}

export function WorkspacePage() {
  const { preference, resolved, cycle } = useWorkspaceTheme()
  const keyboardHeldOpen = useWorkspaceViewport()
  const resolvedRef = useRef(resolved)
  resolvedRef.current = resolved
  const [machines, machinesRef, setMachines] = useLiveState<Machine[]>([])
  const [selectedMachine, selectedMachineRef, setSelectedMachine] = useLiveState<string | null>(null)
  const [tabs, tabsRef, setTabs] = useLiveState<WorkspaceTab[]>([])
  const [activeId, activeIdRef, setActiveId] = useLiveState<string | null>(null)
  const [, activeByMachineRef, setActiveByMachine] = useLiveState<Record<string, string>>({})
  const [presets, presetsRef, setPresets] = useLiveState<Preset[]>([])
  const [initializing, setInitializing] = useState(true)
  const [fatal, setFatal] = useState('')
  const [opening, setOpening] = useState('')
  const [openMenu, openMenuRef, setOpenMenu] = useLiveState<'launch' | 'more' | null>(null)
  const [networkOpen, setNetworkOpen] = useState(false)
  const [presetOpen, setPresetOpen] = useState(false)
  const [securityOpen, setSecurityOpen] = useState(false)
  const network = useNetworkMonitor(tabs, activeId, networkOpen)

  const persistTabState = () => {
    try {
      sessionStorage.setItem(TAB_STATE_KEY, JSON.stringify({
        activeId: activeIdRef.current,
        activeByMachine: activeByMachineRef.current,
      }))
    } catch {
      // The server remains the source of truth for the tab directory.
    }
  }

  const rememberActive = (id: string | null) => {
    if (!id) return
    const tab = tabsRef.current.find((item) => item.id === id)
    if (tab?.machine) setActiveByMachine({ ...activeByMachineRef.current, [tab.machine]: id })
  }

  const activate = (id: string) => {
    setActiveId(id)
    rememberActive(id)
    const tab = tabsRef.current.find((item) => item.id === id)
    if (tab && machinesRef.current.length > 1 && tab.machine && tab.machine !== selectedMachineRef.current) {
      setSelectedMachine(tab.machine)
      try { localStorage.setItem(MACHINE_KEY, tab.machine) } catch { /* Current selection still applies. */ }
    }
    persistTabState()
    setOpenMenu(null)
    requestAnimationFrame(focusActiveTerminal)
  }

  const ensureMachineReady = async (name: string | null): Promise<boolean> => {
    const machine = machinesRef.current.find((item) => item.name === name)
    if (!machine?.wakeable) return false
    const toastID = `machine-ready-${name}`
    const timer = window.setTimeout(() => {
      toast.loading(`Connecting to ${name}; waking it if asleep…`, { id: toastID })
    }, 250)
    try {
      const result = await workspaceAPI<{ woke?: boolean }>(`/api/machines/${encodeURIComponent(name || '')}/ready`, { method: 'POST' })
      setMachines(machinesRef.current.map((item) => item.name === name ? { ...item, healthy: true, failStreak: 0 } : item))
      return Boolean(result.woke)
    } finally {
      clearTimeout(timer)
      toast.dismiss(toastID)
    }
  }

  const openTab = async ({ presetId, title, command }: OpenTabArgs = {}) => {
    const preset = presetId ? presetsRef.current.find((item) => item.id === presetId) : null
    const targetMachine = (preset?.machine || selectedMachineRef.current) ?? null
    const woke = await ensureMachineReady(targetMachine)
    if (woke && targetMachine) reloadMachinePanes(targetMachine)
    const body: Record<string, string> = {}
    if (presetId) body.presetId = presetId
    if (title) body.title = title
    if (command) body.command = command
    if (selectedMachineRef.current) body.machine = selectedMachineRef.current
    const serverTab = await workspaceAPI<TerminalTab>('/api/tabs', postBody(body))
    const tab = toWorkspaceTab({ ...serverTab, machine: serverTab.machine || '' }, resolvedRef.current)
    if (!tab) {
      toast.error('The terminal address is invalid. Close it and open it again.')
      return
    }
    setTabs([...tabsRef.current.filter((item) => item.id !== tab.id), tab])
    if (tab.machine && machinesRef.current.length > 1 && tab.machine !== selectedMachineRef.current) {
      setSelectedMachine(tab.machine)
      try { localStorage.setItem(MACHINE_KEY, tab.machine) } catch { /* Current selection still applies. */ }
    }
    activate(tab.id)
  }

  const openLauncher = async (key: string, args: OpenTabArgs) => {
    if (opening) return
    setOpening(key)
    setOpenMenu(null)
    try {
      await openTab(args)
    } catch (cause) {
      toast.error(errorMessage(cause, 'Could not open terminal'))
    } finally {
      setOpening('')
    }
  }

  const selectMachine = async (name: string) => {
    setOpenMenu(null)
    try {
      const unchanged = name === selectedMachineRef.current
      const woke = await ensureMachineReady(name)
      if (woke) reloadMachinePanes(name)
      if (unchanged) return
      rememberActive(activeIdRef.current)
      setSelectedMachine(name)
      try { localStorage.setItem(MACHINE_KEY, name) } catch { /* Current selection still applies. */ }
      const currentTabs = tabsRef.current
      const keep = currentTabs.find((tab) => tab.id === activeIdRef.current && tab.machine === name)
      let next = keep ?? null
      if (!next) {
        const rememberedID = activeByMachineRef.current[name]
        next = rememberedTabOnMachine(currentTabs, name, rememberedID)
        if (!next && rememberedID) {
          const updated = { ...activeByMachineRef.current }
          delete updated[name]
          setActiveByMachine(updated)
        }
        next ??= lastTabOnMachine(currentTabs, machinesRef.current, name)
      }
      setActiveId(next?.id ?? null)
      rememberActive(next?.id ?? null)
      persistTabState()
      requestAnimationFrame(focusActiveTerminal)
    } catch (cause) {
      toast.error(errorMessage(cause, 'Connection failed'))
    }
  }

  const closeTab = async (id: string) => {
    try {
      await workspaceAPI(`/api/tabs/${encodeURIComponent(id)}`, { method: 'DELETE' })
    } catch {
      toast.error('Could not close tab. Please try again.')
      return
    }
    const pane = document.querySelector<HTMLIFrameElement>(`.terminal-pane[data-id="${CSS.escape(id)}"]`)
    if (pane) pane.src = 'about:blank'
    const nextTabs = tabsRef.current.filter((tab) => tab.id !== id)
    setTabs(nextTabs)
    const remembered = { ...activeByMachineRef.current }
    for (const [machine, rememberedID] of Object.entries(remembered)) {
      if (rememberedID === id) delete remembered[machine]
    }
    setActiveByMachine(remembered)
    const nextActive = tabAfterClose(nextTabs, machinesRef.current, selectedMachineRef.current, id, activeIdRef.current)
    setActiveId(nextActive)
    rememberActive(nextActive)
    persistTabState()
  }

  const loadPresets = async () => {
    const data = await workspaceAPI<{ presets?: Preset[] }>('/api/presets')
    setPresets(data.presets ?? [])
  }

  const initialize = async () => {
    setInitializing(true)
    setFatal('')
    try {
      const machineData = await workspaceAPI<{ machines?: Machine[] }>('/api/machines')
      const machineList = machineData.machines ?? []
      setMachines(machineList)
      const fallback = machineList.find((machine) => machine.default) ?? machineList[0]
      if (!fallback) throw new Error('No terminal machine is configured.')
      let savedMachine: string | null = null
      try { savedMachine = localStorage.getItem(MACHINE_KEY) } catch { savedMachine = null }
      setSelectedMachine(machineList.some((machine) => machine.name === savedMachine) ? savedMachine : fallback.name)

      const data = await workspaceAPI<{ tabs?: TerminalTab[] }>('/api/tabs')
      const serverTabs = data.tabs ?? []
      const restored = serverTabs
        .map((tab) => toWorkspaceTab({ ...tab, machine: tab.machine || '' }, resolvedRef.current))
        .filter((tab): tab is WorkspaceTab => Boolean(tab))
      if (restored.length !== serverTabs.length) toast.error('A saved terminal has an invalid address. Close it and open it again.')
      setTabs(restored)
      const saved = savedTabState()
      setActiveByMachine(validActiveByMachine(restored, saved?.activeByMachine))
      let nextActive: string | null = restored.find((tab) => tab.id === saved?.activeId)?.id ?? restored[0]?.id ?? null
      const active = restored.find((tab) => tab.id === nextActive)
      if (active?.machine && machineList.length > 1) setSelectedMachine(active.machine)
      setActiveId(nextActive)
      rememberActive(nextActive)
      persistTabState()
      if (serverTabs.length === 0) {
        nextActive = null
        await openTab({ title: 'shell' })
      } else if (!restored.length) {
        throw new Error('The saved terminal address is invalid. Close it and open it again.')
      }
    } catch (cause) {
      setFatal(errorMessage(cause, 'Could not open terminal'))
    } finally {
      setInitializing(false)
    }
  }

  useEffect(() => {
    void initialize()
    loadPresets().catch((cause) => toast.error(errorMessage(cause, 'Could not load presets')))
  }, [])

  const visibleTabs = tabsOnMachine(tabs, machines, selectedMachine)
  useEffect(() => {
    document.querySelector('.terminal-tab[data-active="true"]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }, [activeId, selectedMachine, tabs.length])

  const repairTerminals = () => {
    if (!tabsRef.current.length) {
      toast.error('No open terminals')
      return
    }
    for (const pane of document.querySelectorAll<HTMLIFrameElement>('.terminal-pane')) {
      try { pane.contentWindow?.postMessage({ __shellgate: 'repair-display', reason: 'menu' }, window.location.origin) } catch { /* Ignore loading frames. */ }
    }
    if (isCoarseWorkspace()) requestAnimationFrame(focusActiveTerminal)
  }

  const cycleWorkspaceTheme = () => {
    cycle()
    if (isCoarseWorkspace()) requestAnimationFrame(focusActiveTerminal)
  }

  const toggleNetwork = () => {
    setNetworkOpen((value) => !value)
    if (isCoarseWorkspace()) requestAnimationFrame(focusActiveTerminal)
  }

  const launchItems = [
    { key: 'shell', label: 'shell', hint: `Open an empty shell${machines.length > 1 && selectedMachine ? ` · ${selectedMachine}` : ''}`, args: { title: 'shell' } satisfies OpenTabArgs },
    ...presets.map((preset) => ({
      key: preset.id,
      label: preset.name,
      hint: preset.command + (machines.length > 1 && preset.machine ? ` · ${preset.machine}` : ''),
      args: { presetId: preset.id, title: preset.name } satisfies OpenTabArgs,
    })),
  ]

  const onTabKeyDown = (event: React.KeyboardEvent, index: number) => {
    let next = index
    if (event.key === 'ArrowRight') next = (index + 1) % visibleTabs.length
    else if (event.key === 'ArrowLeft') next = (index - 1 + visibleTabs.length) % visibleTabs.length
    else if (event.key === 'Home') next = 0
    else if (event.key === 'End') next = visibleTabs.length - 1
    else return
    event.preventDefault()
    const tab = visibleTabs[next]
    if (tab) {
      activate(tab.id)
      document.getElementById(`tab-${tab.id}`)?.focus()
    }
  }

  return (
    <div className="workspace-shell">
      <div className="workspace-top-shell" onClickCapture={(event) => {
        if (!(event.target as Element).closest('#btnLaunchMenu,#btnMoreMenu')) setOpenMenu(null)
      }}>
        <header className="workspace-top" aria-label="Terminal workspace toolbar">
          <DropdownMenu modal={false} open={openMenu === 'launch'} onOpenChange={(open) => setOpenMenu((current) => open ? 'launch' : (current === 'launch' ? null : current))}>
            <DropdownMenuTrigger asChild>
              <Button id="btnLaunchMenu" className="mobile-control size-11" type="button" variant="ghost" size="icon-sm" aria-label="Launch" title="Launch">
                <Menu />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="start"
              className="w-72"
              onFocusOutside={(event) => event.preventDefault()}
              onCloseAutoFocus={(event) => {
                if (isCoarseWorkspace()) {
                  event.preventDefault()
                  if (!openMenuRef.current) focusActiveTerminal()
                }
              }}
            >
              {machines.length > 1 && (
                <>
                  <DropdownMenuLabel>Machine</DropdownMenuLabel>
                  {machines.map((machine) => (
                    <DropdownMenuItem key={machine.name} onSelect={() => void selectMachine(machine.name)}>
                      <Check className={machine.name === selectedMachine ? 'size-4' : 'size-4 opacity-0'} />
                      <span className="min-w-0 flex-1 truncate">{machine.name}</span>
                      {!machine.healthy && <span className="text-xs text-warning">May be unavailable</span>}
                    </DropdownMenuItem>
                  ))}
                  <DropdownMenuSeparator />
                </>
              )}
              <DropdownMenuLabel>Launch</DropdownMenuLabel>
              {launchItems.map((item) => (
                <DropdownMenuItem key={item.key} disabled={Boolean(opening)} onSelect={() => void openLauncher(item.key, item.args)}>
                  {item.key === 'shell' ? <Plus /> : <Terminal />}
                  <span className="grid min-w-0 flex-1 gap-0.5">
                    <span className="truncate">{item.label}</span>
                    <span className="truncate text-xs text-muted-foreground">{item.hint}</span>
                  </span>
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>

          <div className="workspace-machines desktop-controls" id="machines">
            {machines.length > 1 && machines.map((machine) => (
              <Button
                key={machine.name}
                type="button"
                variant={machine.name === selectedMachine ? 'secondary' : 'ghost'}
                size="sm"
                className={machine.healthy ? '' : 'opacity-55'}
                title={machine.target + (machine.healthy ? '' : ' · May be unavailable')}
                aria-label={`${machine.name}${machine.healthy ? '' : ' (may be unavailable)'}`}
                onClick={() => void selectMachine(machine.name)}
              >
                {machine.name}
              </Button>
            ))}
          </div>

          <div className="workspace-launchers desktop-controls" id="launchers">
            {launchItems.map((item) => (
              <Button
                key={item.key}
                type="button"
                variant={item.key === 'shell' ? 'secondary' : 'outline'}
                size="sm"
                title={item.hint}
                disabled={Boolean(opening)}
                onClick={() => void openLauncher(item.key, item.args)}
              >
                {item.key === 'shell' && <Plus />}
                {item.label}
              </Button>
            ))}
          </div>

          <div className="terminal-tabs" id="tabs" role="tablist" aria-label="Open terminals">
            {visibleTabs.map((tab, index) => (
              <div className="terminal-tab" data-active={tab.id === activeId} key={tab.id}>
                <button
                  className="terminal-tab-select"
                  id={`tab-${tab.id}`}
                  type="button"
                  role="tab"
                  aria-selected={tab.id === activeId}
                  aria-label={tab.title}
                  tabIndex={tab.id === activeId ? 0 : -1}
                  onClick={() => activate(tab.id)}
                  onKeyDown={(event) => onTabKeyDown(event, index)}
                >
                  {tab.title}
                </button>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-xs"
                  className="mr-0.5 size-7"
                  title={`Close ${tab.title}`}
                  aria-label={`Close ${tab.title}`}
                  onClick={() => void closeTab(tab.id)}
                >
                  <X />
                </Button>
              </div>
            ))}
          </div>

          <div className="workspace-rail desktop-controls" id="rail">
            <Button id="btnTheme" type="button" variant="ghost" size="sm" title="Theme: system, light or dark" onClick={cycle}>
              <SunMoon />{THEME_LABEL[preference]}
            </Button>
            <Button id="btnNet" type="button" variant="ghost" size="sm" title="Network diagnostics" onClick={() => setNetworkOpen((value) => !value)}>
              <Activity />Network
            </Button>
            <Button id="btnSecurity" type="button" variant="ghost" size="sm" title="Account security" onClick={() => setSecurityOpen(true)}>
              <ShieldCheck />Security
            </Button>
            <Button id="btnPresets" type="button" variant="ghost" size="sm" title="Manage presets" onClick={() => setPresetOpen(true)}>
              <ListPlus />Presets
            </Button>
          </div>

          <DropdownMenu modal={false} open={openMenu === 'more'} onOpenChange={(open) => setOpenMenu((current) => open ? 'more' : (current === 'more' ? null : current))}>
            <DropdownMenuTrigger asChild>
              <Button id="btnMoreMenu" className="mobile-control size-11" type="button" variant="ghost" size="icon-sm" aria-label="More" title="More">
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="end"
              className="w-64"
              onFocusOutside={(event) => event.preventDefault()}
              onCloseAutoFocus={(event) => {
                if (isCoarseWorkspace()) {
                  event.preventDefault()
                  if (!openMenuRef.current) focusActiveTerminal()
                }
              }}
            >
              <DropdownMenuLabel>More</DropdownMenuLabel>
              <DropdownMenuItem id="menuSecurity" onSelect={() => setSecurityOpen(true)}><ShieldCheck />Account security</DropdownMenuItem>
              <DropdownMenuItem onSelect={cycleWorkspaceTheme}><SunMoon />Theme<span className="ml-auto text-xs text-muted-foreground">{THEME_LABEL[preference]}</span></DropdownMenuItem>
              <DropdownMenuItem onSelect={toggleNetwork}><Activity />Network diagnostics</DropdownMenuItem>
              <DropdownMenuItem onSelect={repairTerminals}><Wrench />Repair display</DropdownMenuItem>
              <DropdownMenuItem onSelect={() => setPresetOpen(true)}><ListPlus />Manage presets</DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </header>
      </div>

      {openMenu && <div className="menu-scrim" aria-hidden="true" onPointerDown={() => setOpenMenu(null)} />}

      <main className="terminal-stage" id="stage">
        {tabs.map((tab) => (
          <iframe
            className="terminal-pane"
            data-active={tab.id === activeId}
            data-id={tab.id}
            data-machine={tab.machine}
            key={tab.id}
            src={tab.src}
            title={`Terminal: ${tab.title}`}
            allow="clipboard-read; clipboard-write"
            tabIndex={tab.id === activeId ? 0 : -1}
            aria-hidden={tab.id !== activeId}
            style={{ display: tab.id === activeId ? 'block' : 'none' }}
            onLoad={(event) => {
              if (keyboardHeldOpen.current) {
                event.currentTarget.contentWindow?.postMessage({ __shellgate: 'keyboard', open: true }, window.location.origin)
              }
            }}
          />
        ))}
        {initializing ? (
          <div className="workspace-empty" aria-busy="true">
            <div className="workspace-empty-content grid gap-3">
              <Skeleton className="h-7 w-32" />
              <Skeleton className="h-4 w-56" />
            </div>
          </div>
        ) : fatal ? (
          <div className="workspace-empty">
            <div className="workspace-empty-content grid justify-items-start gap-4">
              <Notice tone="error">{fatal}</Notice>
              <Button type="button" variant="outline" onClick={() => void initialize()}>Retry</Button>
            </div>
          </div>
        ) : visibleTabs.length === 0 ? (
          <div className="workspace-empty" id="empty">
            <div className="workspace-empty-content">
              <h1 className="workspace-empty-title">Terminal</h1>
              <p className="workspace-empty-copy">{machines.length > 1 && selectedMachine ? `+ shell · on ${selectedMachine} · New terminal` : '+ shell · New terminal'}</p>
              <Button className="mt-5" variant="outline" disabled={Boolean(opening)} onClick={() => void openLauncher('shell', { title: 'shell' })}>
                <Plus />Open shell
              </Button>
            </div>
          </div>
        ) : null}
      </main>

      {networkOpen && (
        <NetworkPanel
          snapshot={network.snapshot}
          gate={network.gateStats}
          echo={network.activeEcho}
          live={network.live}
          tabs={tabs}
          activeID={activeId}
          onClose={() => setNetworkOpen(false)}
        />
      )}
      <PresetDialog
        open={presetOpen}
        onOpenChange={setPresetOpen}
        presets={presets}
        onPresetsChange={setPresets}
        machines={machines}
        selectedMachine={selectedMachine}
      />
      <AccountSecurityDialog open={securityOpen} onOpenChange={setSecurityOpen} />
      <Toaster />
    </div>
  )
}
