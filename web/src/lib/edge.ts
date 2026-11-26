import type { Edge } from '../types'

export const EDGE_PREF_KEY = 'shellgate_edge'
export const PROBE_TIMEOUT_MS = 3000

export async function probeEdge(origin: string, timeoutMs = PROBE_TIMEOUT_MS): Promise<number | null> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeoutMs)
  const started = performance.now()
  try {
    const response = await fetch(`${origin}/api/edge-probe`, {
      method: 'GET',
      mode: 'cors',
      cache: 'no-store',
      credentials: 'omit',
      signal: controller.signal,
    })
    if (!response.ok) return null
    return performance.now() - started
  } catch {
    return null
  } finally {
    clearTimeout(timer)
  }
}

export async function chooseEdge(edges: Edge[], preferred: string | null): Promise<string | null> {
  if (preferred && edges.some((edge) => edge.host === preferred)) {
    const ms = await probeEdge(`https://${preferred}`)
    if (ms !== null) return preferred
  }
  const results = await Promise.all(
    edges.map(async (edge) => ({
      host: edge.host,
      ms: await probeEdge(edge.origin || `https://${edge.host}`),
    })),
  )
  let best: { host: string; ms: number } | null = null
  for (const result of results) {
    if (result.ms !== null && (!best || result.ms < best.ms)) best = { host: result.host, ms: result.ms }
  }
  return best?.host ?? null
}
