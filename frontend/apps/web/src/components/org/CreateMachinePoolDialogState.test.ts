import { describe, expect, it } from 'vitest'

import {
  derivedMemoryTotalCapPlaceholder,
  machinePoolCreateRequest,
  machinePoolFormDefaults,
  machinePoolFormValid,
} from './CreateMachinePoolDialogState'

describe('machine pool memory inputs', () => {
  it('converts GB values to integer MB and derives the total from converted machine memory', () => {
    const values = {
      ...machinePoolFormDefaults,
      name: 'default',
      image: 'alpine:latest',
      secretId: 'secret_1',
      memoryGb: '1.25',
      maxMachines: '3',
    }

    expect(machinePoolFormValid(values)).toBe(true)
    expect(derivedMemoryTotalCapPlaceholder(values.memoryGb, values.maxMachines)).toBe('3.75')
    expect(machinePoolCreateRequest(values)).toMatchObject({
      default_machine_memory_mb: 1280,
      max_total_memory_mb: 3840,
      max_machine_memory_mb: 1280,
    })
  })
})
