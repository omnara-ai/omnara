import { describe, expect, it } from 'vitest'

import { emptyProviderOptions } from '@/components/machines/machineOverrides'

import {
  createEmptyBasicConfigDraft,
  serializeBasicConfigDraft,
} from './agentConfigBasicSerialization'

describe('basic agent machine memory', () => {
  it('serializes GB input as integer machine_memory_mb', () => {
    const config = {
      ...createEmptyBasicConfigDraft(),
      instruction: 'Run tasks',
      providerConfig: 'provider',
      modelName: 'model',
      machineSources: [
        {
          id: 'source_1',
          kind: 'pool' as const,
          name: 'default',
          provider: 'unikraft',
          managementKind: 'tenant',
          defaultCwd: '',
          initialNumMachines: '',
          maxMachines: '',
          machineCpu: '',
          machineMemoryGb: '1.5',
          providerOptions: emptyProviderOptions,
          envRows: [],
          secretEnvRows: [],
        },
      ],
    }

    expect(serializeBasicConfigDraft(config)).toContain('    machine_memory_mb: 1536\n')
  })
})
