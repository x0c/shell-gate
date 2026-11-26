export type AuthMode = 'local' | 'supabase' | 'sso' | 'oidc' | string

export interface AuthConfig {
  authMode: AuthMode
  ssoEnabled?: boolean
  ssoIssuer?: string
  supabaseUrl?: string
  anonKey?: string
  edges?: Edge[]
}

export interface Edge {
  name?: string
  host: string
  origin?: string
}

export interface SignInResult {
  mfaRequired?: boolean
  status?: string
}

export interface Machine {
  name: string
  target: string
  title?: string
  default: boolean
  healthy: boolean
  failStreak: number
  lastError?: string
  wakeable: boolean
  transport?: string
}

export interface Preset {
  id: string
  name: string
  command: string
  machine: string
  sortOrder?: number
}

export interface TerminalTab {
  id: string
  title: string
  path: string
  machine: string
}

export interface WorkspaceTab extends TerminalTab {
  src: string
}

export interface RttStats {
  count: number
  lastMs: number
  minMs: number
  maxMs: number
  avgMs: number
  p50Ms: number
  p95Ms: number
  jitterMs: number
}

export interface TabNetReport {
  tabId: string
  at: number
  connected?: boolean
  closeCode?: number
  reconnects?: number
  downBps?: number
  upBps?: number
  downBytes?: number
  upBytes?: number
  echo?: RttStats
  cols?: number
  rows?: number
  renderPerSec?: number
  longTaskMs?: number
  idleSeconds?: number
}

export interface ProbeStats {
  tcp: RttStats
  ttfb: RttStats
  okCount: number
  failCount: number
  failStreak: number
  lastStatus: number
  lastError?: string
  intervalSec: number
}

export interface UpstreamSnapshot {
  name: string
  target: string
  probe: ProbeStats
  handshake: RttStats
  conns: {
    active: number
    dialFails: number
    proxyFails: number
  }
  throughput: {
    downBps10s: number
    upBps10s: number
    downBps1s: number
    upBps1s: number
    downBps60s: number
    upBps60s: number
    downBytesTotal: number
    upBytesTotal: number
  }
}

export interface NetSnapshot {
  upstreams: UpstreamSnapshot[]
  gate: {
    uptimeSec: number
    activeTabs: number
    heapMB: number
    goroutines: number
  }
}

export interface TOTPStatus {
  email: string
  enabled: boolean
  recoveryCodesRemaining: number
}

export interface TOTPSetup {
  setupId: string
  secret: string
  qr: string
}

export interface TOTPRecovery {
  enabled: boolean
  recoveryCodes: string[]
}
