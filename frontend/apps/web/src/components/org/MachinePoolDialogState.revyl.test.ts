import type { MachinePool } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import {
  machinePoolCreateRequest,
  machinePoolFormAfterProviderChange,
  machinePoolFormDefaults,
  machinePoolFormFromPool,
  machinePoolUpdateRequest,
} from './MachinePoolDialogState'
import { machinePoolFormValid } from './machinePoolValidation'

describe('Revyl machine pools', () => {
  it('creates a macOS pool without machine sizes and edits its lifetime', () => {
    const values = {
      ...machinePoolFormAfterProviderChange(machinePoolFormDefaults, 'revyl'),
      name: 'revyl-macos',
      maxMachines: '2',
      secretId: 'sec_revyl',
    }
    expect(machinePoolFormValid(values)).toBe(true)
    expect(machinePoolCreateRequest(values)).toEqual({
      name: 'revyl-macos',
      provider: 'revyl',
      provider_auth_secret_id: 'sec_revyl',
      max_total_machines: 2,
      default_machine_provider_options: {},
      runtime_protection_enabled: false,
    })
    expect(
      machinePoolCreateRequest({ ...values, image: ' 24h ' }).default_machine_provider_options,
    ).toEqual({
      lifetime: '24h',
    })

    const pool = machinePool({
      provider: 'revyl',
      default_machine_cpu: null,
      default_machine_memory_mb: null,
      default_machine_provider_options: { lifetime: '2h', startup_script: 'echo ready' },
      max_total_cpu: null,
      max_total_memory_mb: null,
      max_machine_cpu: null,
      max_machine_memory_mb: null,
    })
    const edited = machinePoolFormFromPool(pool)
    if (edited === null) throw new Error('expected Revyl form values')
    expect(edited.image).toBe('2h')
    const request = machinePoolUpdateRequest(pool, { ...edited, image: '6h' })
    expect(request.default_machine_provider_options).toEqual({
      lifetime: '6h',
      startup_script: 'echo ready',
    })
    expect(request).not.toHaveProperty('max_total_cpu')
    expect(request).not.toHaveProperty('default_machine_memory_mb')
  })
})

function machinePool(overrides: Partial<MachinePool> = {}): MachinePool {
  return {
    id: 'mpool_1',
    org_id: 'org_1',
    name: 'pool',
    management_kind: 'tenant',
    description: '',
    provider: 'unikraft',
    default_machine_cpu: 1,
    default_machine_memory_mb: 1024,
    default_machine_env: {},
    default_machine_secret_env: {},
    default_machine_provider_options: { image: 'image', metro: 'sfo' },
    default_cwd: '',
    provider_auth_secret_id: 'sec_provider',
    provider_config: {},
    runtime_protection_enabled: false,
    max_total_machines: 3,
    max_total_cpu: 3,
    max_total_memory_mb: 3072,
    min_machine_cpu: null,
    min_machine_memory_mb: null,
    max_machine_cpu: 1,
    max_machine_memory_mb: 1024,
    delete_after_idle_minutes: null,
    metadata: {},
    created_at: '2026-08-18T00:00:00Z',
    updated_at: '2026-08-18T00:00:00Z',
    ...overrides,
  }
}
