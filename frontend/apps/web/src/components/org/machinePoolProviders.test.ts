import { describe, expect, it } from 'vitest'

import { machinePool } from '@/test/fixtures'

import {
  machinePoolConnectionCheckRequest,
  machinePoolCreatableProviders,
  machinePoolFormFromPool,
  machinePoolUpdateRequest,
} from './MachinePoolDialogState'
import { machinePoolScopeValue } from './machinePoolProviders'

describe('machine pool scope display', () => {
  it.each([
    [undefined, 'omnara'],
    ['', 'omnara'],
    ['  ', 'omnara'],
    ['agents', 'agents'],
    [' agents ', 'agents'],
  ])('shows the effective Modal app for %j', (app, expected) => {
    expect(machinePoolScopeValue('modal', app === undefined ? {} : { app })).toBe(expected)
  })

  it('does not apply the Modal default to a Blaxel workspace', () => {
    expect(machinePoolScopeValue('blaxel', { workspace: '' })).toBe('')
  })
})

describe('Docker machine pools', () => {
  const providerConfig = {
    api_base_url: 'https://10.0.5.21:2376',
    runtime: 'runsc',
    allowed_images: ['buildpack-deps:24.04-curl'],
  }
  const pool = machinePool({
    provider: 'docker',
    provider_auth_secret_id: 'sec_docker',
    provider_config: providerConfig,
    default_machine_cpu: 2,
    default_machine_memory_mb: 4096,
    default_machine_provider_options: { image: 'buildpack-deps:24.04-curl' },
    max_total_cpu: 8,
    max_total_memory_mb: 16384,
    max_machine_cpu: 2,
    max_machine_memory_mb: 4096,
  })

  it('is created through the API only', () => {
    expect(machinePoolCreatableProviders.map((option) => option.value)).not.toContain('docker')
  })

  it('keeps the saved Docker settings when checking and saving an edit', () => {
    const values = machinePoolFormFromPool(pool)
    if (!values) throw new Error('Expected Docker pool form')
    expect(values).toMatchObject({ image: 'buildpack-deps:24.04-curl', cpu: '2' })
    expect(machinePoolConnectionCheckRequest(values, pool.provider_config)).toMatchObject({
      provider: 'docker',
      provider_auth_secret_id: 'sec_docker',
      provider_config: providerConfig,
    })
    const request = machinePoolUpdateRequest(pool, { ...values, cpu: '1' })
    expect(request.provider_config).toBeUndefined()
    expect(request).toMatchObject({
      default_machine_cpu: 1,
      default_machine_provider_options: { image: 'buildpack-deps:24.04-curl' },
    })
  })
})
