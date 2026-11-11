import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import {
  machinePoolCreateRequest,
  machinePoolFormAfterProviderChange,
  machinePoolFormDefaults,
} from './MachinePoolDialogState'
import { MachinePoolProviderLogo } from './MachinePoolProviderLogo'
import { machinePoolFormValid } from './machinePoolValidation'

describe('bead machine pools', () => {
  it('uses the base template by default and leaves sizing to the provider', () => {
    const values = {
      ...machinePoolFormAfterProviderChange(machinePoolFormDefaults, 'bead'),
      name: 'bead-pool',
      secretId: 'sec_bead',
    }
    expect(machinePoolFormValid(values)).toBe(true)
    const request = machinePoolCreateRequest(values)
    expect(request).toMatchObject({
      provider: 'bead',
      default_machine_provider_options: {},
      max_machine_cpu: 2,
      max_machine_memory_mb: 4096,
    })
    expect(request).not.toHaveProperty('default_machine_cpu')
    expect(request).not.toHaveProperty('default_machine_memory_mb')
    expect(
      machinePoolCreateRequest({ ...values, image: 'computer' }).default_machine_provider_options,
    ).toEqual({ template: 'computer' })
  })
})

it('renders the bead brand mark', () => {
  const markup = renderToStaticMarkup(<MachinePoolProviderLogo provider="bead" />)
  expect(markup).toContain('viewBox="0 0 64 64"')
  expect(markup).toContain('M18 12v40h15')
})
