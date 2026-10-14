import type { MachinePool } from '@omnara/sdk'

import { formatMemoryGb } from '@/lib/machine-memory'

export function formatPoolMachines(pool: MachinePool) {
  return `${pool.usage?.machines ?? '—'} / ${pool.max_total_machines}`
}

/** CPU and memory in use against the pool's quotas; undefined when neither is capped. */
export function formatPoolResources(pool: MachinePool) {
  const parts: string[] = []
  if (pool.max_total_cpu !== null) {
    parts.push(`${pool.usage?.cpu ?? '—'} / ${pool.max_total_cpu} vCPU`)
  }
  if (pool.max_total_memory_mb !== null) {
    parts.push(
      `${formatMemoryGb(pool.usage?.memory_mb) ?? '—'} / ${formatMemoryGb(pool.max_total_memory_mb)}`,
    )
  }
  return parts.length === 0 ? undefined : parts.join(' · ')
}
