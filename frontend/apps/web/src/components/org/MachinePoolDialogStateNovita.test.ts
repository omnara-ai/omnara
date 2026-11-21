import { expect, it } from 'vitest'

import { machinePool } from '@/test/fixtures'

import {
  machinePoolCreateRequest,
  machinePoolFormAfterProviderChange,
  machinePoolFormDefaults,
  machinePoolFormFromPool,
  machinePoolUpdateRequest,
} from './MachinePoolDialogState'
import { machinePoolFormValid } from './machinePoolValidation'

it('creates and edits a Novita template pool', () => {
  const values = {
    ...machinePoolFormAfterProviderChange(machinePoolFormDefaults, 'novita'),
    name: 'novita-pool',
    image: ' base ',
    maxMachines: '3',
    secretId: 'sec_novita',
  }
  expect(machinePoolFormValid(values)).toBe(true)
  expect(machinePoolCreateRequest(values)).toMatchObject({
    provider: 'novita',
    default_machine_provider_options: { template: 'base' },
    max_total_cpu: 24,
    max_total_memory_mb: 24576,
    max_machine_cpu: 8,
    max_machine_memory_mb: 8192,
  })
  const pool = machinePool({
    provider: 'novita',
    default_machine_cpu: null,
    default_machine_memory_mb: null,
    default_machine_provider_options: { template: 'base', startup_script: 'echo ready' },
    max_machine_cpu: 2,
    max_machine_memory_mb: 2048,
  })
  const form = machinePoolFormFromPool(pool)
  if (!form) throw new Error('Expected Novita pool form')
  expect(form).toMatchObject({ image: 'base', cpu: '2', memoryGb: '2' })
  const request = machinePoolUpdateRequest(pool, { ...form, image: 'my-template' })
  expect(request.default_machine_provider_options).toEqual({
    template: 'my-template',
    startup_script: 'echo ready',
  })
  expect(request.provider_config).toBeUndefined()
})
