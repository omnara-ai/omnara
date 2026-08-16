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

function validDraft() {
  return {
    ...createEmptyBasicConfigDraft(),
    instruction: 'Help the user make progress.',
    providerConfig: 'Production OpenAI',
    modelName: 'GPT 5',
  }
}

describe('basic agent config resource names', () => {
  it.each([
    ['provider config', { providerConfig: ' Production OpenAI' }],
    ['configured model', { modelName: 'GPT 5 ' }],
  ])('rejects boundary whitespace in %s references', (_case, patch) => {
    expect(serializeBasicConfigDraft({ ...validDraft(), ...patch })).toBe('')
  })

  it('preserves accepted resource names exactly', () => {
    expect(serializeBasicConfigDraft(validDraft())).toContain(
      'provider_config: "Production OpenAI"\n  name: "GPT 5"',
    )
  })

  it('rejects rather than trims machine references', () => {
    const draft = validDraft()
    draft.machineSources = [
      {
        id: 'machine-source-1',
        kind: 'machine',
        name: ' Primary Machine',
        provider: '',
        managementKind: '',
        defaultCwd: '',
        initialNumMachines: '',
        maxMachines: '',
        machineCpu: '',
        machineMemoryGb: '',
        providerOptions: emptyProviderOptions,
        envRows: [],
        secretEnvRows: [],
      },
    ]
    expect(serializeBasicConfigDraft(draft)).toBe('')
  })

  it('rejects rather than trims MCP server keys', () => {
    const draft = validDraft()
    draft.mcpServers = [
      {
        id: 'mcp-1',
        name: ' github',
        url: 'https://example.test/mcp',
        permission: { mode: 'ask', parameters: {} },
        defaultEnabled: true,
        authType: 'none',
        secretId: '',
        service: '',
        region: '',
      },
    ]
    expect(serializeBasicConfigDraft(draft)).toBe('')
  })
})
