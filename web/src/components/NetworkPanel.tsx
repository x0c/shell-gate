import { Button } from '@x0c/ui'
import { Minus, Plus, X } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import type { NetSnapshot, RttStats, TabNetReport, UpstreamSnapshot, WorkspaceTab } from '../types'
import { fmtBps, fmtBytes, fmtDuration, fmtMs, grade, type MetricTone } from '../lib/netmon'

interface MetricRow {
  key: string
  value: string
  tone?: MetricTone
}

function MetricGroup({ title, subtitle, rows, note }: { title: string; subtitle?: string; rows: MetricRow[]; note?: string }) {
  return (
    <section className="metric-group">
      <h3 className="metric-heading">
        <span>{title}</span>
        {subtitle && <span className="metric-subheading">{subtitle}</span>}
      </h3>
      <div>
        {rows.map((row, index) => (
          <div className="metric-row" key={`${row.key}-${index}`}>
            <span className="metric-key">{row.key}</span>
            <span className={`metric-value ${row.tone ? `metric-${row.tone}` : ''}`} title={row.value}>
              {row.value}
            </span>
          </div>
        ))}
      </div>
      {note && <p className="metric-note">{note}</p>}
    </section>
  )
}

function upstreamFor(snapshot: NetSnapshot | null, machine: string | undefined): UpstreamSnapshot | null {
  const upstreams = snapshot?.upstreams ?? []
  return upstreams.find((upstream) => upstream.name === machine) ?? upstreams[0] ?? null
}

export function NetworkPanel({
  snapshot,
  gate,
  echo,
  live,
  tabs,
  activeID,
  onClose,
}: {
  snapshot: NetSnapshot | null
  gate: RttStats
  echo: RttStats | null
  live: Array<{ tab: WorkspaceTab; net: TabNetReport }>
  tabs: WorkspaceTab[]
  activeID: string | null
  onClose: () => void
}) {
  const [collapsed, setCollapsed] = useState(false)
  const activeTab = tabs.find((tab) => tab.id === activeID)
  const upstream = upstreamFor(snapshot, activeTab?.machine)
  const probe = upstream?.probe
  const hop = probe && probe.tcp.count > 0 ? probe.tcp : upstream?.handshake
  const totalEstimate = (gate.p50Ms || 0) + (hop?.p50Ms || 0)
  const connection = (navigator as Navigator & { connection?: { effectiveType?: string } }).connection
  const groups: ReactNode[] = []

  groups.push(
    <MetricGroup
      key="responsiveness"
      title="Responsiveness"
      subtitle="End to end"
      rows={[
        { key: 'Key echo median', value: echo ? fmtMs(echo.p50Ms) : '—', tone: echo ? grade(echo.p50Ms, 120, 300) : '' },
        { key: 'Key echo P95', value: echo ? fmtMs(echo.p95Ms) : '—', tone: echo ? grade(echo.p95Ms, 200, 500) : '' },
        { key: 'Echo samples', value: String(echo?.count ?? 0) },
        { key: 'Combined network delay', value: fmtMs(totalEstimate), tone: grade(totalEstimate, 100, 250) },
      ]}
      note="Key echo includes time spent in the remote program, as well as network delay."
    />,
  )
  groups.push(
    <MetricGroup
      key="device"
      title="Device ↔ Gateway"
      subtitle="Public network"
      rows={[
        { key: 'RTT median', value: fmtMs(gate.p50Ms), tone: grade(gate.p50Ms, 60, 150) },
        { key: 'RTT P95 / latest', value: `${fmtMs(gate.p95Ms)} / ${fmtMs(gate.lastMs)}`, tone: grade(gate.p95Ms, 100, 250) },
        { key: 'Jitter', value: fmtMs(gate.jitterMs, gate.count > 1), tone: grade(gate.jitterMs, 20, 60) },
        { key: 'Samples', value: String(gate.count) },
        {
          key: 'Network',
          value: navigator.onLine === false ? 'Offline' : connection?.effectiveType || 'Online',
          tone: navigator.onLine === false ? 'bad' : 'good',
        },
      ]}
    />,
  )

  for (const machine of snapshot?.upstreams ?? []) {
    const current = machine.probe
    const rows: MetricRow[] = [
      { key: 'Terminal host', value: machine.target || '—' },
      { key: 'Connection RTT', value: fmtMs(current.tcp.p50Ms), tone: grade(current.tcp.p50Ms, 30, 120) },
      { key: 'Jitter', value: fmtMs(current.tcp.jitterMs, current.tcp.count > 1), tone: grade(current.tcp.jitterMs, 15, 50) },
      { key: 'First byte incl. server', value: fmtMs(current.ttfb.p50Ms), tone: grade(current.ttfb.p50Ms, 60, 200) },
      { key: 'Terminal handshake', value: fmtMs(machine.handshake.p50Ms), tone: grade(machine.handshake.p50Ms, 30, 120) },
      { key: 'Probes passed / failed', value: `${current.okCount} / ${current.failCount}`, tone: current.failStreak > 0 ? 'bad' : 'good' },
      {
        key: 'Upstream response',
        value: current.lastStatus ? `HTTP ${current.lastStatus}` : 'No response',
        tone: current.lastStatus === 200 ? 'good' : current.lastStatus ? 'warn' : 'bad',
      },
      { key: 'Active connections', value: String(machine.conns.active) },
      {
        key: 'Failed dial / proxy',
        value: `${machine.conns.dialFails} / ${machine.conns.proxyFails}`,
        tone: machine.conns.dialFails + machine.conns.proxyFails > 0 ? 'warn' : '',
      },
    ]
    if (current.failStreak > 0) rows.push({ key: 'Consecutive failures', value: String(current.failStreak), tone: 'bad' })
    if (current.lastError) rows.push({ key: 'Latest error', value: current.lastError, tone: 'bad' })
    const multi = (snapshot?.upstreams.length ?? 0) > 1
    groups.push(
      <MetricGroup
        key={`upstream-${machine.name}`}
        title={multi ? `Gateway ↔ ${machine.name}` : 'Gateway ↔ Terminal'}
        subtitle="Private connection"
        rows={rows}
        note={current.intervalSec > 0 ? `Probed every ${current.intervalSec}s.` : 'Active probes are disabled; samples come from terminal connections.'}
      />,
    )
    const throughput = machine.throughput
    groups.push(
      <MetricGroup
        key={`throughput-${machine.name}`}
        title={multi ? `Throughput · ${machine.name}` : 'Gateway throughput'}
        rows={[
          { key: 'Output last 10s', value: fmtBps(throughput.downBps10s) },
          { key: 'Input last 10s', value: fmtBps(throughput.upBps10s) },
          { key: 'Output / input now', value: `${fmtBps(throughput.downBps1s)} / ${fmtBps(throughput.upBps1s)}` },
          { key: 'Output / input 1m', value: `${fmtBps(throughput.downBps60s)} / ${fmtBps(throughput.upBps60s)}` },
          { key: 'Total output / input', value: `${fmtBytes(throughput.downBytesTotal)} / ${fmtBytes(throughput.upBytesTotal)}` },
        ]}
      />,
    )
  }

  groups.push(
    <MetricGroup
      key="terminals"
      title="Terminals"
      subtitle={live.length ? `${live.length} reporting` : undefined}
      rows={live.length
        ? live.flatMap(({ tab, net }) => {
            const rows: MetricRow[] = [
              {
                key: tab.title + (tab.id === activeID ? ' (active)' : ''),
                value: net.connected ? 'Connected' : 'Disconnected',
                tone: net.connected ? 'good' : 'bad',
              },
              { key: '  Output / input', value: `${fmtBps(net.downBps)} / ${fmtBps(net.upBps)}` },
              {
                key: '  Echo · size',
                value: `${net.echo?.count ? fmtMs(net.echo.p50Ms) : '—'} · ${net.cols ?? 0}×${net.rows ?? 0}`,
                tone: net.echo?.count ? grade(net.echo.p50Ms, 120, 300) : '',
              },
            ]
            if ((net.longTaskMs ?? 0) > 0) rows.push({ key: '  Browser stalls', value: `${net.longTaskMs} ms`, tone: grade(net.longTaskMs, 100, 400) })
            if ((net.reconnects ?? 0) > 0) rows.push({ key: '  Reconnects last 1m', value: `${net.reconnects} times`, tone: 'warn' })
            return rows
          })
        : [{ key: 'Status', value: 'No terminal reports yet' }]}
      note={live.length ? undefined : 'New terminals need a few seconds to report data.'}
    />,
  )
  groups.push(
    <MetricGroup
      key="gateway"
      title="Gateway"
      rows={snapshot
        ? [
            { key: 'Uptime', value: fmtDuration(snapshot.gate.uptimeSec) },
            { key: 'Terminal tabs', value: String(snapshot.gate.activeTabs) },
            { key: 'Memory', value: `${snapshot.gate.heapMB} MB` },
            { key: 'Goroutines', value: String(snapshot.gate.goroutines) },
          ]
        : [{ key: 'Status', value: 'Could not read data', tone: 'bad' }]}
    />,
  )

  return (
    <aside className="network-card" aria-label="Network diagnostics">
      <div className="network-head" onPointerDown={(event) => event.preventDefault()}>
        <span className="network-title">Network diagnostics</span>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          aria-label={collapsed ? 'Expand network diagnostics' : 'Collapse network diagnostics'}
          title={collapsed ? 'Expand' : 'Collapse'}
          onClick={() => setCollapsed((value) => !value)}
        >
          {collapsed ? <Plus /> : <Minus />}
        </Button>
        <Button type="button" variant="ghost" size="icon-sm" aria-label="Close network diagnostics" title="Close" onClick={onClose}>
          <X />
        </Button>
      </div>
      {!collapsed && <div className="network-body">{groups}</div>}
    </aside>
  )
}
