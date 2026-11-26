import { describe, expect, it } from 'vitest'
import { createRttTracker, fmtBytes, fmtDuration, fmtMs, grade } from './netmon'

describe('network metrics', () => {
  it('uses one-clock RTT samples and reports percentiles and jitter', () => {
    const tracker = createRttTracker(3)
    for (const value of [10, 20, 30, 40]) tracker.add(value)
    const stats = tracker.stats()
    expect(stats.count).toBe(4)
    expect(stats.p50Ms).toBe(30)
    expect(stats.p95Ms).toBe(40)
    expect(stats.minMs).toBe(20)
    expect(stats.jitterMs).toBeGreaterThan(0)
  })

  it('distinguishes a real zero from missing samples', () => {
    expect(fmtMs(0, true)).toBe('0 ms')
    expect(fmtMs(0, false)).toBe('—')
    expect(fmtMs(12.34)).toBe('12.3 ms')
    expect(fmtMs(1200)).toBe('1.20 s')
    expect(fmtBytes(0)).toBe('0 B')
    expect(fmtDuration(3660)).toBe('1h 1m')
    expect(grade(100, 120, 300)).toBe('good')
    expect(grade(200, 120, 300)).toBe('warn')
    expect(grade(400, 120, 300)).toBe('bad')
  })
})
