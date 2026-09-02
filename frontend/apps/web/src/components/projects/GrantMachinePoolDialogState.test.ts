import { describe, expect, it } from 'vitest'

import { machinePool, projectMachinePoolGrant } from '@/test/fixtures'

import {
  emptyPoolGrantDraft,
  poolGrantCreateRequest,
  poolGrantDraftFromGrant,
  poolGrantUpdateRequest,
} from './GrantMachinePoolDialogState'

const pool = machinePool({ id: 'pool_1', provider: 'unknown', management_kind: 'tenant' })

const grant = projectMachinePoolGrant({
  description: '',
  default_machine_cpu: null,
  default_machine_memory_mb: 9000,
  default_machine_env_overlay: {},
  default_machine_secret_env_overlay: {},
  default_machine_provider_options_overlay: {},
  default_cwd: '',
  max_total_machines: null,
  max_total_cpu: null,
  max_total_memory_mb: 0,
  min_machine_cpu: null,
  min_machine_memory_mb: null,
  max_machine_cpu: null,
  max_machine_memory_mb: 128,
  delete_after_idle_minutes: 45,
})

describe('project grant memory inputs', () => {
  it('preserves untouched MB values, clears empty values, and converts changed GB values', () => {
    const draft = poolGrantDraftFromGrant({
      grant,
      machine_pool: pool,
    })

    expect(draft).toMatchObject({
      memoryGb: '8.79',
      maxTotalMemoryGb: '0',
      minMachineMemoryGb: '',
      maxMachineMemoryGb: '0.13',
      deleteAfterIdleMinutes: '45',
    })
    expect(poolGrantUpdateRequest(pool, grant, draft)).toMatchObject({
      default_machine_memory_mb: 9000,
      max_total_memory_mb: 0,
      min_machine_memory_mb: null,
      max_machine_memory_mb: 128,
      delete_after_idle_minutes: 45,
    })

    expect(
      poolGrantUpdateRequest(pool, grant, {
        ...draft,
        memoryGb: '',
        maxMachineMemoryGb: '1.5',
        deleteAfterIdleMinutes: '',
      }),
    ).toMatchObject({
      default_machine_memory_mb: null,
      max_machine_memory_mb: 1536,
      delete_after_idle_minutes: null,
    })
  })

  it('converts new GB overrides at the API boundary', () => {
    expect(
      poolGrantCreateRequest(pool, {
        ...emptyPoolGrantDraft(),
        memoryGb: '1.5',
        maxTotalMemoryGb: '0',
        deleteAfterIdleMinutes: '30',
      }),
    ).toMatchObject({
      default_machine_memory_mb: 1536,
      max_total_memory_mb: 0,
      delete_after_idle_minutes: 30,
    })
  })
})
