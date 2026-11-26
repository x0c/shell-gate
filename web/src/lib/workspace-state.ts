import type { Machine, WorkspaceTab } from '../types'

export function tabsOnMachine(tabs: WorkspaceTab[], machines: Machine[], selected: string | null): WorkspaceTab[] {
  if (machines.length <= 1 || !selected) return tabs
  return tabs.filter((tab) => tab.machine === selected)
}

export function lastTabOnMachine(tabs: WorkspaceTab[], machines: Machine[], selected: string | null): WorkspaceTab | null {
  const visible = tabsOnMachine(tabs, machines, selected)
  return visible.at(-1) ?? null
}

export function rememberedTabOnMachine(
  tabs: WorkspaceTab[],
  machine: string,
  rememberedID: string | undefined,
): WorkspaceTab | null {
  if (!rememberedID) return null
  return tabs.find((tab) => tab.id === rememberedID && tab.machine === machine) ?? null
}

export function validActiveByMachine(
  tabs: WorkspaceTab[],
  value: unknown,
): Record<string, string> {
  const result: Record<string, string> = {}
  if (!value || typeof value !== 'object') return result
  for (const [machine, id] of Object.entries(value)) {
    if (typeof id === 'string' && tabs.some((tab) => tab.id === id && tab.machine === machine)) {
      result[machine] = id
    }
  }
  return result
}

export function tabAfterClose(
  tabs: WorkspaceTab[],
  machines: Machine[],
  selected: string | null,
  closedID: string,
  activeID: string | null,
): string | null {
  if (activeID !== closedID) return activeID
  return lastTabOnMachine(tabs.filter((tab) => tab.id !== closedID), machines, selected)?.id ?? null
}
