import { useEffect, useRef, useState } from 'react'
import type { NetSnapshot, RttStats, TabNetReport, WorkspaceTab } from '../types'
import { workspaceAPI } from './api'

export const NET_PING_IDLE_MS = 15000
export const NET_PING_OPEN_MS = 2000
export const NET_PUSH_MS = 20000
export const NET_TAB_STALE_MS = 8000

export const EMPTY_RTT: RttStats = {
  count: 0,
  lastMs: 0,
  minMs: 0,
  maxMs: 0,
  avgMs: 0,
  p50Ms: 0,
  p95Ms: 0,
  jitterMs: 0,
}

export function createRttTracker(size: number) {
  const buffer: number[] = []
  let jitter = 0
  let previous: number | null = null
  let total = 0
  let last = 0
  return {
    add(ms: number) {
      if (!(ms >= 0) || !Number.isFinite(ms)) return
      buffer.push(ms)
      if (buffer.length > size) buffer.shift()
      if (previous !== null) jitter += (Math.abs(ms - previous) - jitter) / 16
      previous = ms
      last = ms
      total += 1
    },
    stats(): RttStats {
      if (!buffer.length) return { ...EMPTY_RTT, count: total }
      const sorted = [...buffer].sort((a, b) => a - b)
      const sum = sorted.reduce((a, b) => a + b, 0)
      const percentile = (p: number) => sorted[Math.min(sorted.length - 1, Math.max(0, Math.ceil(p * sorted.length) - 1))]
      const round = (value: number) => Math.round(value * 10) / 10
      return {
        count: total,
        lastMs: round(last),
        minMs: round(sorted[0]),
        maxMs: round(sorted.at(-1) ?? 0),
        avgMs: round(sum / sorted.length),
        p50Ms: round(percentile(0.5)),
        p95Ms: round(percentile(0.95)),
        jitterMs: round(jitter),
      }
    },
  }
}

export function fmtMs(value: number | undefined, hasSamples = false): string {
  if (!(value && value > 0)) return hasSamples ? '0 ms' : '—'
  return value >= 1000 ? `${(value / 1000).toFixed(2)} s` : `${Math.round(value * 10) / 10} ms`
}

export function fmtBps(value: number | undefined): string {
  if (!(value && value > 0)) return '0 B/s'
  if (value >= 1024 * 1024) return `${(value / 1024 / 1024).toFixed(2)} MB/s`
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KB/s`
  return `${Math.round(value)} B/s`
}

export function fmtBytes(value: number | undefined): string {
  if (!(value && value > 0)) return '0 B'
  if (value >= 1024 ** 3) return `${(value / 1024 ** 3).toFixed(2)} GB`
  if (value >= 1024 ** 2) return `${(value / 1024 ** 2).toFixed(1)} MB`
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${value} B`
}

export function fmtDuration(seconds: number | undefined): string {
  if (!(seconds && seconds > 0)) return '—'
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days) return `${days}d ${hours}h`
  if (hours) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

export type MetricTone = 'good' | 'warn' | 'bad' | ''

export function grade(value: number | undefined, good: number, warn: number): MetricTone {
  if (!(value && value > 0)) return ''
  if (value <= good) return 'good'
  if (value <= warn) return 'warn'
  return 'bad'
}

function clientID(): string {
  try {
    let id = sessionStorage.getItem('__shellgate_client')
    if (!id) {
      id = `${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`
      sessionStorage.setItem('__shellgate_client', id)
    }
    return id
  } catch {
    return 'anon'
  }
}

interface NetworkInformation {
  effectiveType?: string
}

function connectionInfo(): NetworkInformation {
  return (navigator as Navigator & { connection?: NetworkInformation }).connection ?? {}
}

export function useNetworkMonitor(tabs: WorkspaceTab[], activeID: string | null, open: boolean) {
  const [snapshot, setSnapshot] = useState<NetSnapshot | null>(null)
  const [gateStats, setGateStats] = useState<RttStats>(EMPTY_RTT)
  const [tabNet, setTabNet] = useState<Map<string, TabNetReport>>(new Map())
  const gateTracker = useRef(createRttTracker(120))
  const openRef = useRef(open)
  const tabsRef = useRef(tabs)
  const activeRef = useRef(activeID)
  const tabNetRef = useRef(tabNet)
  const id = useRef(clientID())
  openRef.current = open
  tabsRef.current = tabs
  activeRef.current = activeID
  tabNetRef.current = tabNet

  useEffect(() => {
    const receive = (event: MessageEvent) => {
      if (event.origin !== window.location.origin) return
      const data = event.data as Partial<TabNetReport> & { __shellgate?: string }
      if (!data || data.__shellgate !== 'netmon' || typeof data.tabId !== 'string') return
      setTabNet((current) => new Map(current).set(data.tabId as string, { ...data, tabId: data.tabId as string, at: Date.now() }))
    }
    window.addEventListener('message', receive)
    return () => window.removeEventListener('message', receive)
  }, [])

  useEffect(() => {
    let stopped = false
    let timer = 0
    const run = async () => {
      if (stopped) return
      if (document.visibilityState === 'visible') {
        const started = performance.now()
        try {
          const response = await fetch('/api/netmon/ping', { credentials: 'same-origin', cache: 'no-store' })
          await response.text()
          if (response.ok) gateTracker.current.add(performance.now() - started)
        } catch {
          // Timeouts are not RTT samples.
        }
        if (!stopped) setGateStats(gateTracker.current.stats())
      }
      if (!stopped) timer = window.setTimeout(run, openRef.current ? NET_PING_OPEN_MS : NET_PING_IDLE_MS)
    }
    void run()
    return () => {
      stopped = true
      clearTimeout(timer)
    }
  }, [open])

  useEffect(() => {
    if (!open) return
    let stopped = false
    let timer = 0
    const refresh = async () => {
      try {
        const value = await workspaceAPI<NetSnapshot>('/api/netmon')
        if (!stopped) setSnapshot(value)
      } catch {
        if (!stopped) setSnapshot(null)
      }
      if (!stopped) timer = window.setTimeout(refresh, 2000)
    }
    void refresh()
    return () => {
      stopped = true
      clearTimeout(timer)
    }
  }, [open])

  useEffect(() => {
    const push = async () => {
      const now = Date.now()
      const live = tabsRef.current
        .map((tab) => ({ tab, net: tabNetRef.current.get(tab.id) }))
        .filter((row): row is { tab: WorkspaceTab; net: TabNetReport } => Boolean(row.net && now - row.net.at < NET_TAB_STALE_MS))
      const gate = gateTracker.current.stats()
      if (!live.length && gate.count === 0) return
      const active = live.find((row) => row.tab.id === activeRef.current && row.net.echo)
        ?? live.find((row) => row.net.echo && row.net.echo.count > 0)
      try {
        await fetch('/api/netmon/sample', {
          method: 'POST',
          credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            clientId: id.current,
            device: window.matchMedia('(pointer: coarse)').matches ? 'mobile' : 'desktop',
            online: navigator.onLine !== false,
            netType: connectionInfo().effectiveType || '',
            gateRtt: gate,
            echoRtt: active?.net.echo,
            reconnects: live.reduce((sum, row) => sum + (row.net.reconnects || 0), 0),
            tabs: live.map(({ tab, net }) => ({
              tabId: tab.id,
              title: tab.title,
              machine: tab.machine || '',
              connected: Boolean(net.connected),
              downBps: net.downBps || 0,
              upBps: net.upBps || 0,
              downBytes: net.downBytes || 0,
              upBytes: net.upBytes || 0,
              echoMs: net.echo?.p50Ms || 0,
              cols: net.cols || 0,
              rows: net.rows || 0,
              renderPerSec: net.renderPerSec || 0,
              longTaskMs: net.longTaskMs || 0,
              reconnects: net.reconnects || 0,
              closeCode: net.closeCode || 0,
              idleSeconds: net.idleSeconds,
            })),
          }),
        })
      } catch {
        // Archiving diagnostics must never block terminal use.
      }
    }
    const timer = window.setInterval(push, NET_PUSH_MS)
    return () => clearInterval(timer)
  }, [])

  const now = Date.now()
  const live = tabs
    .map((tab) => ({ tab, net: tabNet.get(tab.id) }))
    .filter((row): row is { tab: WorkspaceTab; net: TabNetReport } => Boolean(row.net && now - row.net.at < NET_TAB_STALE_MS))
  const active = live.find((row) => row.tab.id === activeID && row.net.echo)
    ?? live.find((row) => row.net.echo && row.net.echo.count > 0)

  return { snapshot, gateStats, live, activeEcho: active?.net.echo ?? null }
}
