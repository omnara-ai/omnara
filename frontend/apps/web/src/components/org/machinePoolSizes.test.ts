import { describe, expect, it } from 'vitest'

import {
  machineSizeClassLabel,
  machineSizeDrafts,
  machineSizeSelection,
  machineSizeValid,
  providerDefaultSizeValue,
} from './machinePoolSizes'

describe('machine size classes', () => {
  it('maps drafts to the offered boxd sizes', () => {
    expect(machineSizeSelection('boxd', { cpu: '2', memoryGb: '8' })).toBe('2x8192')
    expect(machineSizeSelection('boxd', { cpu: ' 2 ', memoryGb: '8.0' })).toBe('2x8192')
    expect(machineSizeSelection('boxd', { cpu: '', memoryGb: '' })).toBe(providerDefaultSizeValue)
    expect(machineSizeSelection('boxd', { cpu: '1', memoryGb: '1' })).toBe('')
    expect(machineSizeSelection('boxd', { cpu: '2', memoryGb: '' })).toBe('')
  })

  it('leaves providers with free sizing alone', () => {
    expect(machineSizeSelection('unikraft', { cpu: '1', memoryGb: '1' })).toBe('')
    expect(machineSizeValid('unikraft', { cpu: '1', memoryGb: '1' })).toBe(true)
    expect(machineSizeValid('boxd', { cpu: '1', memoryGb: '1' })).toBe(false)
  })

  it('turns a picker value back into drafts', () => {
    expect(machineSizeDrafts('boxd', '4x16384')).toEqual({ cpu: '4', memoryGb: '16' })
    expect(machineSizeDrafts('boxd', providerDefaultSizeValue)).toEqual({ cpu: '', memoryGb: '' })
    expect(machineSizeClassLabel({ cpu: 1, memoryMb: 4096 })).toBe('1 vCPU · 4 GB')
  })
})
