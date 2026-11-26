import { BrandMark, Button, Notice } from '@x0c/ui'
import { useCallback, useEffect, useState } from 'react'
import { requestJSON } from '../lib/api'
import { chooseEdge, EDGE_PREF_KEY } from '../lib/edge'
import { replaceLocation } from '../lib/navigation'
import type { AuthConfig } from '../types'

export function EdgePickPage() {
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)

  const connect = useCallback(async () => {
    setError('')
    try {
      const config = await requestJSON<AuthConfig>('/api/config', { credentials: 'same-origin', cache: 'no-store' })
      const edges = Array.isArray(config.edges) ? config.edges : []
      if (!edges.length) {
        replaceLocation('/')
        return
      }
      let preferred: string | null = null
      try { preferred = localStorage.getItem(EDGE_PREF_KEY) } catch { preferred = null }
      const host = await chooseEdge(edges, preferred)
      if (!host) {
        setError('No connection is available. Check your network and retry.')
        return
      }
      try { localStorage.setItem(EDGE_PREF_KEY, host) } catch { /* Keep the current choice for this visit. */ }
      replaceLocation(`https://${host}/`)
    } catch {
      setError('Could not load connection settings. Check your connection and retry.')
    }
  }, [])

  useEffect(() => { void connect() }, [connect, attempt])

  return (
    <main className="edge-shell">
      <div className="edge-content">
        <BrandMark size="lg" label="shell-gate" />
        <h1 className="m-0 font-mono text-xl font-medium tracking-[-0.04em]">shell-gate</h1>
        {error ? (
          <>
            <Notice tone="error">{error}</Notice>
            <Button type="button" variant="outline" onClick={() => setAttempt((value) => value + 1)}>Retry</Button>
          </>
        ) : (
          <p className="m-0 text-sm text-muted-foreground" role="status" aria-live="polite">Choosing the fastest connection…</p>
        )}
      </div>
    </main>
  )
}
