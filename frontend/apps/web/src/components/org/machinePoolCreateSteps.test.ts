import { describe, expect, it } from 'vitest'

import {
  machinePoolCreateSteps,
  machinePoolCreateStepValid,
  machinePoolMachineSizeLabel,
  machinePoolTotalLabel,
} from './machinePoolCreateSteps'
import {
  machinePoolFormAfterProviderChange,
  machinePoolFormDefaults,
  type MachinePoolFormValues,
} from './MachinePoolDialogState'
import {
  machinePoolFieldErrors,
  machinePoolFieldGroup,
  machinePoolFieldGroups,
  machinePoolFormValid,
} from './machinePoolValidation'

const blaxel = {
  ...machinePoolFormDefaults,
  name: 'default',
  image: 'omnara/agent-sandbox',
  providerScope: 'acme',
  secretId: 'secret_1',
}

describe('machinePoolCreateStepValid', () => {
  it('checks only the fields each step owns', () => {
    expect(machinePoolCreateStepValid('provider', blaxel)).toBe(true)
    expect(machinePoolCreateStepValid('provider', { ...blaxel, secretId: '' })).toBe(false)
    expect(machinePoolCreateStepValid('provider', { ...blaxel, name: '' })).toBe(false)
    expect(machinePoolCreateStepValid('provider', { ...blaxel, image: '' })).toBe(true)

    expect(machinePoolCreateStepValid('image', blaxel)).toBe(true)
    expect(machinePoolCreateStepValid('image', { ...blaxel, image: ' ' })).toBe(false)
    expect(machinePoolCreateStepValid('image', { ...blaxel, providerScope: '' })).toBe(false)
    const tenki = machinePoolFormAfterProviderChange(blaxel, 'tenki')
    expect(machinePoolCreateStepValid('image', { ...tenki, image: '' })).toBe(true)

    expect(machinePoolCreateStepValid('capacity', blaxel)).toBe(true)
    expect(machinePoolCreateStepValid('capacity', { ...blaxel, location: '' })).toBe(false)
    expect(machinePoolCreateStepValid('capacity', { ...blaxel, memoryGb: '0' })).toBe(false)
    expect(machinePoolCreateStepValid('capacity', { ...blaxel, maxMachines: '-1' })).toBe(false)

    expect(machinePoolCreateStepValid('environment', { ...blaxel, startupScript: '' })).toBe(true)
    expect(
      machinePoolCreateStepValid('environment', {
        ...blaxel,
        envRows: [{ id: 'row_1', key: '', value: 'x' }],
      }),
    ).toBe(false)
  })

  it('fails the capacity step for advanced limits that would block create', () => {
    const invalidLimits: Partial<MachinePoolFormValues>[] = [
      { maxMachineMemoryGb: '0' },
      { maxMachineMemoryGb: '-1' },
      { maxTotalMemoryGb: '-1' },
      { minMachineMemoryGb: '-1' },
      { deleteAfterIdleMinutes: '4' },
    ]
    for (const limit of invalidLimits) {
      const values = { ...blaxel, ...limit }
      expect(machinePoolFormValid(values)).toBe(false)
      expect(machinePoolCreateStepValid('capacity', values)).toBe(false)
    }
    const freestyle = machinePoolFormAfterProviderChange(blaxel, 'freestyle')
    expect(machinePoolCreateStepValid('capacity', { ...freestyle, maxMachineCpu: '0' })).toBe(false)
    expect(machinePoolCreateStepValid('capacity', { ...freestyle, minMachineCpu: '1.5' })).toBe(
      false,
    )
    expect(machinePoolCreateStepValid('capacity', { ...freestyle, maxTotalCpu: '-1' })).toBe(false)
  })

  it('makes create valid exactly when every step is valid', () => {
    const cases: MachinePoolFormValues[] = [
      blaxel,
      { ...blaxel, name: '' },
      { ...blaxel, providerScope: '' },
      { ...blaxel, memoryGb: '0' },
      { ...blaxel, maxMachineMemoryGb: '0' },
      { ...blaxel, maxMachines: '1.5' },
      { ...blaxel, secretEnvRows: [{ id: 'row_1', key: 'TOKEN', secretId: '' }] },
      machinePoolFormAfterProviderChange(blaxel, 'freestyle'),
    ]
    for (const values of cases) {
      const stepsValid = machinePoolCreateSteps.every((step) =>
        machinePoolCreateStepValid(step, values),
      )
      expect(stepsValid).toBe(machinePoolFormValid(values))
    }
  })
})

describe('machinePoolFieldErrors', () => {
  it('explains each invalid field and assigns it to one step', () => {
    expect(machinePoolFieldErrors(blaxel)).toEqual({})
    const errors = machinePoolFieldErrors({ ...blaxel, maxMachineMemoryGb: '0' })
    expect(errors).toEqual({
      maxMachineMemoryGb: 'Enter a size greater than 0 GB, or leave it empty.',
    })
    expect(machinePoolFieldGroup.maxMachineMemoryGb).toBe('capacity')
    expect(machinePoolCreateSteps).toEqual(machinePoolFieldGroups)
  })

  it('skips fields a cluster manages', () => {
    const cluster = { ...blaxel, name: '', secretId: '', maxMachines: '', maxTotalMemoryGb: '-1' }
    expect(machinePoolFieldErrors(cluster, 'cluster-edit')).toEqual({})
    expect(machinePoolFormValid(cluster, 'cluster-edit')).toBe(true)
  })

  it('flags a size whose derived pool total overflows', () => {
    const errors = machinePoolFieldErrors({ ...blaxel, memoryGb: '2000000', maxMachines: '2' })
    expect(errors.memoryGb).toMatch(/Too large for this many machines/)
  })
})

describe('machine pool capacity labels', () => {
  it('describes memory-only machines and their pool total', () => {
    expect(machinePoolMachineSizeLabel(blaxel)).toBe('1 GB')
    expect(machinePoolTotalLabel(blaxel)).toBe('3 GB total')
    expect(machinePoolTotalLabel({ ...blaxel, maxTotalMemoryGb: '2' })).toBe('2 GB total')
    expect(machinePoolMachineSizeLabel({ ...blaxel, memoryGb: 'x' })).toBeUndefined()
  })

  it('includes vCPU for providers that size it', () => {
    const freestyle = machinePoolFormAfterProviderChange(blaxel, 'freestyle')
    expect(machinePoolMachineSizeLabel(freestyle)).toBe('2 vCPU · 4 GB')
    expect(machinePoolTotalLabel(freestyle)).toBe('6 vCPU · 12 GB total')
  })
})
