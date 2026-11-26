import { describe, expect, it } from 'vitest'
import type { Machine, WorkspaceTab } from '../types'
import { lastTabOnMachine, rememberedTabOnMachine, tabAfterClose, tabsOnMachine, validActiveByMachine } from './workspace-state'

const machines: Machine[] = [
  { name: 'linux', target: 'linux', default: true, healthy: true, failStreak: 0, wakeable: false },
  { name: 'mac', target: 'mac', default: false, healthy: true, failStreak: 0, wakeable: true },
]
const tabs: WorkspaceTab[] = [
  { id: 'l1', title: 'l1', path: '/t/l1/', src: '/t/l1/', machine: 'linux' },
  { id: 'm1', title: 'm1', path: '/t/m1/', src: '/t/m1/', machine: 'mac' },
  { id: 'l2', title: 'l2', path: '/t/l2/', src: '/t/l2/', machine: 'linux' },
]

describe('per-machine tab state', () => {
  it('filters visible tabs without unmounting other machines from the directory', () => {
    expect(tabsOnMachine(tabs, machines, 'linux').map((tab) => tab.id)).toEqual(['l1', 'l2'])
    expect(tabsOnMachine(tabs, machines, 'mac').map((tab) => tab.id)).toEqual(['m1'])
    expect(tabs).toHaveLength(3)
  })

  it('restores the remembered tab before the latest tab on that machine', () => {
    expect(rememberedTabOnMachine(tabs, 'linux', 'l1')?.id).toBe('l1')
    expect(lastTabOnMachine(tabs, machines, 'linux')?.id).toBe('l2')
    expect(rememberedTabOnMachine(tabs, 'linux', 'deleted')).toBeNull()
  })

  it('drops stale session selections but retains valid per-machine choices', () => {
    expect(validActiveByMachine(tabs, { linux: 'l1', mac: 'deleted', other: 'm1' })).toEqual({ linux: 'l1' })
  })

  it('stays on the same machine after closing the active tab', () => {
    expect(tabAfterClose(tabs, machines, 'linux', 'l2', 'l2')).toBe('l1')
    expect(tabAfterClose(tabs, machines, 'mac', 'm1', 'm1')).toBeNull()
    expect(tabAfterClose(tabs, machines, 'linux', 'l1', 'm1')).toBe('m1')
  })
})
