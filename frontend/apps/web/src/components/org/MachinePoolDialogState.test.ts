import type { MachinePool } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import {
  derivedMemoryTotalCapPlaceholder,
  machinePoolCreateRequest,
  machinePoolFormDefaults,
  machinePoolFormFromPool,
  machinePoolFormValid,
  machinePoolUpdateRequest,
} from './MachinePoolDialogState'

describe('machine pool memory inputs', () => {
  it('converts GB values to integer MB and derives the total from converted machine memory', () => {
    const values = {
      ...machinePoolFormDefaults,
      name: 'default',
      image: 'alpine:latest',
      workspace: 'workspace',
      secretId: 'secret_1',
      memoryGb: '1.25',
      maxMachines: '3',
      deleteAfterIdleMinutes: '30',
    }

    expect(machinePoolFormValid(values)).toBe(true)
    expect(machinePoolFormValid({ ...values, deleteAfterIdleMinutes: '0' })).toBe(false)
    expect(machinePoolFormValid({ ...values, deleteAfterIdleMinutes: '4' })).toBe(false)
    expect(machinePoolFormValid({ ...values, deleteAfterIdleMinutes: '5' })).toBe(true)
    expect(derivedMemoryTotalCapPlaceholder(values.memoryGb, values.maxMachines)).toBe('3.75')
    expect(machinePoolCreateRequest(values)).toMatchObject({
      provider: 'blaxel',
      provider_config: { workspace: 'workspace' },
      default_machine_provider_options: { region: 'us-pdx-1' },
      default_machine_memory_mb: 1280,
      max_total_memory_mb: 3840,
      max_machine_memory_mb: 1280,
      delete_after_idle_minutes: 30,
    })
  })

  it('allows a pool to be paused with a zero machine quota', () => {
    const values = {
      ...machinePoolFormDefaults,
      name: 'paused',
      image: 'alpine:latest',
      workspace: 'workspace',
      secretId: 'secret_1',
      maxMachines: '0',
    }

    expect(machinePoolFormValid(values)).toBe(true)
    expect(machinePoolCreateRequest(values)).toMatchObject({
      max_total_machines: 0,
      max_total_memory_mb: 0,
    })
    expect(machinePoolCreateRequest(values)).not.toHaveProperty('max_total_cpu')
  })
})

describe('machine pool names', () => {
  const validValues = {
    ...machinePoolFormDefaults,
    image: 'alpine:latest',
    workspace: 'workspace',
    secretId: 'secret_1',
  }

  it('preserves an accepted name exactly on create', () => {
    const values = { ...validValues, name: 'R&D Pool 😀' }

    expect(machinePoolFormValid(values)).toBe(true)
    expect(machinePoolCreateRequest(values).name).toBe('R&D Pool 😀')
  })

  it.each([' pool', 'pool ', 'x'.repeat(65), 'pool\u200dname'])(
    'rejects invalid name %j',
    (name) => {
      expect(machinePoolFormValid({ ...validValues, name })).toBe(false)
    },
  )
})

describe('machine pool edit state', () => {
  it('hydrates every shared Blaxel field and preserves hidden provider settings', () => {
    const pool = machinePool({
      provider: 'blaxel',
      description: 'Original description',
      default_machine_memory_mb: 1537,
      default_machine_env: { BETA: 'two', ALPHA: 'one' },
      default_machine_secret_env: { TOKEN: 'sec_token' },
      default_machine_provider_options: {
        image: 'old-image',
        region: 'old-region',
        startup_script: 'echo old',
        sleep_after_ms: 60_000,
      },
      default_cwd: '/old/workspace',
      provider_config: {
        workspace: 'old-workspace',
        allowed_images: ['old-image'],
        allowed_regions: ['old-region'],
      },
      runtime_protection_enabled: true,
      max_total_machines: 5,
      max_total_memory_mb: 7685,
      min_machine_memory_mb: 513,
      max_machine_memory_mb: 2049,
    })

    const values = machinePoolFormFromPool(pool)

    expect(values).not.toBeNull()
    if (values === null) throw new Error('expected supported provider form values')
    expect(values).toMatchObject({
      name: 'pool',
      description: 'Original description',
      provider: 'blaxel',
      workspace: 'old-workspace',
      image: 'old-image',
      location: 'old-region',
      startupScript: 'echo old',
      cwd: '/old/workspace',
      memoryGb: '1.5',
      maxMachines: '5',
      maxTotalMemoryGb: '7.5',
      minMachineMemoryGb: '0.5',
      maxMachineMemoryGb: '2',
      deleteAfterIdleMinutes: '',
      secretId: 'sec_provider',
      runtimeProtectionEnabled: true,
    })
    expect(values.envRows.map(({ key, value }) => ({ key, value }))).toEqual([
      { key: 'ALPHA', value: 'one' },
      { key: 'BETA', value: 'two' },
    ])
    expect(values.secretEnvRows.map(({ key, secretId }) => ({ key, secretId }))).toEqual([
      { key: 'TOKEN', secretId: 'sec_token' },
    ])

    const request = machinePoolUpdateRequest(pool, {
      ...values,
      name: 'R&D updated 😀',
      description: ' updated description ',
      workspace: 'new-workspace',
      image: 'new-image',
      location: 'new-region',
      startupScript: '',
      cwd: ' /new/workspace ',
      envRows: [],
      secretEnvRows: [],
    })

    expect(request).toMatchObject({
      name: 'R&D updated 😀',
      description: 'updated description',
      default_machine_memory_mb: 1537,
      default_machine_env: {},
      default_machine_secret_env: {},
      default_machine_provider_options: {
        image: 'new-image',
        region: 'new-region',
        sleep_after_ms: 60_000,
      },
      default_cwd: '/new/workspace',
      provider_config: {
        workspace: 'new-workspace',
        allowed_images: ['old-image'],
        allowed_regions: ['old-region'],
      },
      max_total_memory_mb: 7685,
      min_machine_memory_mb: 513,
      max_machine_memory_mb: 2049,
    })
    expect(request.default_machine_provider_options).not.toHaveProperty('startup_script')
  })

  it('uses Daytona max-machine resources as its primary editable size', () => {
    const pool = machinePool({
      provider: 'daytona',
      default_machine_cpu: null,
      default_machine_memory_mb: null,
      default_machine_provider_options: {
        snapshot: 'snapshot-1',
        target: 'us',
      },
      max_total_cpu: 12,
      max_total_memory_mb: 9219,
      max_machine_cpu: 4,
      max_machine_memory_mb: 3073,
    })

    const values = machinePoolFormFromPool(pool)

    if (values === null) throw new Error('expected supported provider form values')
    expect(values).toMatchObject({
      provider: 'daytona',
      image: 'snapshot-1',
      location: 'us',
      cpu: '4',
      memoryGb: '3',
      maxMachineCpu: '',
      maxMachineMemoryGb: '',
    })
    const request = machinePoolUpdateRequest(pool, values)
    expect(request).toMatchObject({
      max_machine_cpu: 4,
      max_machine_memory_mb: 3073,
      max_total_memory_mb: 9219,
    })
    expect(request).not.toHaveProperty('default_machine_cpu')
    expect(request).not.toHaveProperty('default_machine_memory_mb')
  })

  it('serializes only organization-editable fields for a cluster pool', () => {
    const pool = machinePool({
      management_kind: 'cluster',
      provider_auth_secret_id: undefined,
      default_machine_provider_options: {
        image: 'cluster-image',
        metro: 'sfo',
        sleep_after_ms: 60_000,
        startup_script: 'echo old',
      },
      min_machine_cpu: 1,
      min_machine_memory_mb: 512,
      max_machine_cpu: 2,
      max_machine_memory_mb: 2048,
    })
    const values = machinePoolFormFromPool(pool)
    if (values === null) throw new Error('expected supported provider form values')

    const edited = {
      ...values,
      name: '',
      description: 'not editable',
      image: '',
      location: '',
      secretId: '',
      maxMachines: '',
      maxTotalCpu: 'not editable',
      maxTotalMemoryGb: 'not editable',
      startupScript: 'echo new',
      cpu: '2',
      memoryGb: '2',
      minMachineCpu: '1',
      minMachineMemoryGb: '1',
      maxMachineCpu: '4',
      maxMachineMemoryGb: '4',
      deleteAfterIdleMinutes: '15',
    }

    expect(machinePoolFormValid(edited, 'cluster-edit')).toBe(true)
    const request = machinePoolUpdateRequest(pool, edited)
    expect(request).toEqual({
      default_machine_cpu: 2,
      default_machine_memory_mb: 2048,
      default_machine_env: {},
      default_machine_secret_env: {},
      min_machine_cpu: 1,
      min_machine_memory_mb: 1024,
      max_machine_cpu: 4,
      max_machine_memory_mb: 4096,
      delete_after_idle_minutes: 15,
    })
  })

  it('uses only applicable resource fields for cluster providers', () => {
    const blaxel = machinePool({
      management_kind: 'cluster',
      provider: 'blaxel',
      provider_auth_secret_id: undefined,
      default_machine_cpu: null,
      default_machine_memory_mb: 1024,
      default_machine_provider_options: { image: 'image', region: 'us' },
      provider_config: { workspace: 'workspace' },
      max_total_cpu: null,
      min_machine_cpu: null,
      max_machine_cpu: null,
    })
    const blaxelValues = machinePoolFormFromPool(blaxel)
    if (blaxelValues === null) throw new Error('expected Blaxel form values')
    const blaxelRequest = machinePoolUpdateRequest(blaxel, blaxelValues)
    expect(blaxelRequest).toHaveProperty('default_machine_memory_mb', 1024)
    expect(blaxelRequest).toHaveProperty('max_machine_memory_mb', 1024)
    expect(blaxelRequest).not.toHaveProperty('default_machine_cpu')
    expect(blaxelRequest).not.toHaveProperty('min_machine_cpu')
    expect(blaxelRequest).not.toHaveProperty('max_machine_cpu')

    const daytona = machinePool({
      management_kind: 'cluster',
      provider: 'daytona',
      provider_auth_secret_id: undefined,
      default_machine_cpu: null,
      default_machine_memory_mb: null,
      default_machine_provider_options: { snapshot: 'snapshot', target: 'us' },
      max_machine_cpu: 4,
      max_machine_memory_mb: 3072,
    })
    const daytonaValues = machinePoolFormFromPool(daytona)
    if (daytonaValues === null) throw new Error('expected Daytona form values')
    const daytonaRequest = machinePoolUpdateRequest(daytona, daytonaValues)
    expect(daytonaRequest).toHaveProperty('max_machine_cpu', 4)
    expect(daytonaRequest).toHaveProperty('max_machine_memory_mb', 3072)
    expect(daytonaRequest).not.toHaveProperty('default_machine_cpu')
    expect(daytonaRequest).not.toHaveProperty('default_machine_memory_mb')
  })

  it('rejects unsupported and changed providers', () => {
    const unsupported = machinePool({ provider: 'future-provider' })
    expect(machinePoolFormFromPool(unsupported)).toBeNull()

    const pool = machinePool()
    expect(() =>
      machinePoolUpdateRequest(pool, { ...machinePoolFormDefaults, provider: 'daytona' }),
    ).toThrow('machine pool provider cannot be changed')
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
