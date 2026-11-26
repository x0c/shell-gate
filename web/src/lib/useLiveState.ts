import { useCallback, useRef, useState } from 'react'

/** State with a synchronously updated ref for terminal callbacks that must not capture stale tabs. */
export function useLiveState<T>(initial: T) {
  const [value, setValue] = useState(initial)
  const ref = useRef(initial)
  const set = useCallback((next: T | ((current: T) => T)) => {
    const resolved = typeof next === 'function' ? (next as (current: T) => T)(ref.current) : next
    ref.current = resolved
    setValue(resolved)
  }, [])
  return [value, ref, set] as const
}
