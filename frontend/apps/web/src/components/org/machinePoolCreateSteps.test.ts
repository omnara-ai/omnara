import { describe, expect, it } from 'vitest'

import {
  machinePoolCreateStepValid,
  machinePoolMachineSizeLabel,
  machinePoolTotalLabel,
} from './machinePoolCreateSteps'
import {
  machinePoolFormAfterProviderChange,
  machinePoolFormDefaults,
} from './MachinePoolDialogState'

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
    expect(machinePoolCreateStepValid('provider', { ...blaxel, providerScope: '' })).toBe(false)
    expect(machinePoolCreateStepValid('provider', { ...blaxel, name: '' })).toBe(true)

    expect(machinePoolCreateStepValid('pool', blaxel)).toBe(true)
    expect(machinePoolCreateStepValid('pool', { ...blaxel, image: ' ' })).toBe(false)
    expect(machinePoolCreateStepValid('pool', { ...blaxel, name: '' })).toBe(false)

    expect(machinePoolCreateStepValid('capacity', blaxel)).toBe(true)
    expect(machinePoolCreateStepValid('capacity', { ...blaxel, location: '' })).toBe(false)
    expect(machinePoolCreateStepValid('capacity', { ...blaxel, memoryGb: '0' })).toBe(false)
    expect(machinePoolCreateStepValid('capacity', { ...blaxel, maxMachines: '-1' })).toBe(false)

    expect(machinePoolCreateStepValid('startup', { ...blaxel, startupScript: '' })).toBe(true)
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
