import { useSyncExternalStore } from 'react'

import type { UsageBreakdown, UsageMeasure } from '@/components/usage/usage-chart-data'
import { defaultUsageRange, type UsageDateRange } from '@/components/usage/usage-date-range'

export interface UsageViewState {
  range: UsageDateRange
  measure: UsageMeasure
  breakdown: UsageBreakdown
}

/**
 * The org and project usage pages share one selection for the session, so switching
 * between all projects and a single project keeps the range, measure, and breakdown.
 */
let state: UsageViewState = { range: defaultUsageRange(), measure: 'tokens', breakdown: 'model' }
const listeners = new Set<() => void>()

export function useUsageViewState() {
  return useSyncExternalStore(subscribe, getSnapshot)
}

export function updateUsageViewState(patch: Partial<UsageViewState>) {
  state = { ...state, ...patch }
  for (const listener of listeners) listener()
}

function subscribe(onStoreChange: () => void) {
  listeners.add(onStoreChange)
  return () => {
    listeners.delete(onStoreChange)
  }
}

function getSnapshot() {
  return state
}
