import type { MachineSummary } from '@omnara/sdk'

import type { DetailItem } from '@/components/data-table/DetailList'
import { formatDateTime } from '@/lib/format'

/** Human-readable machine state: the lifecycle while it isn't active, else its connection. */
export function machineStatusLabel(machine: MachineSummary) {
  return machine.lifecycle_state === 'active'
    ? machine.connection_state
    : machine.lifecycle_state.replace('_', ' ')
}

export function machineDetailItems(machine: MachineSummary): DetailItem[] {
  return [
    { label: 'ID', value: machine.id, mono: true },
    { label: 'Description', value: machine.description },
    { label: 'Provider', value: machine.provider },
    { label: 'State', value: machine.lifecycle_state.replace('_', ' ') },
    { label: 'Connection', value: machine.connection_state },
    { label: 'Last observed', value: formatDateTime(machine.last_observed_at) },
    { label: 'Created', value: formatDateTime(machine.created_at) },
    { label: 'Updated', value: formatDateTime(machine.updated_at) },
  ]
}
