import { describe, expect, it } from 'vitest'

import { deserializeBasicConfig } from '@/components/agents/agentConfigBasicDeserialization'
import {
  type BasicConfig,
  serializeBasicConfig,
} from '@/components/agents/agentConfigBasicSerialization'
import { emptyProviderOptions } from '@/components/machines/machineOverrides'

const fullConfig: BasicConfig = {
  instruction: 'You are a research assistant.\n\nCite sources.',
  providerConfig: 'anthropic',
  modelName: 'claude-sonnet-5',
  machineSources: [
    {
      id: 'source-1',
      kind: 'pool',
      name: 'default-pool',
      provider: '',
      managementKind: '',
      defaultCwd: '/workspace',
      initialNumMachines: '2',
      maxMachines: '5',
      machineCpu: '4',
      machineMemoryMb: '8192',
      providerOptions: emptyProviderOptions,
      envRows: [{ id: 'env-1', key: 'MODE', value: 'ci' }],
      secretEnvRows: [{ id: 'secret-1', key: 'TOKEN', secretId: 'sec_123' }],
    },
    {
      id: 'source-2',
      kind: 'machine',
      name: 'build-box',
      provider: '',
      managementKind: '',
      defaultCwd: '',
      initialNumMachines: '',
      maxMachines: '',
      machineCpu: '',
      machineMemoryMb: '',
      providerOptions: emptyProviderOptions,
      envRows: [],
      secretEnvRows: [],
    },
  ],
  tools: [
    { name: 'shell', permission: { mode: 'always_ask', parameters: {} } },
    { name: 'browser', permission: { mode: 'allowlist', parameters: { hosts: ['example.com'] } } },
  ],
  mcpServers: [
    {
      id: 'mcp-1',
      name: 'search',
      url: 'https://mcp.example.com',
      permission: { mode: 'always_allow', parameters: {} },
      defaultEnabled: true,
      authType: 'none',
      secretId: '',
      service: '',
      region: '',
    },
    {
      id: 'mcp-2',
      name: 'aws-docs',
      url: 'https://mcp.aws.example.com',
      permission: { mode: 'always_ask', parameters: {} },
      defaultEnabled: false,
      authType: 'sigv4',
      secretId: 'sec_456',
      service: 'execute-api',
      region: 'us-east-1',
    },
    {
      id: 'mcp-3',
      name: 'issues',
      url: 'https://mcp.issues.example.com',
      permission: { mode: 'always_ask', parameters: {} },
      defaultEnabled: true,
      authType: 'bearer',
      secretId: 'sec_789',
      service: '',
      region: '',
    },
  ],
  skillIds: ['skl_1', 'skl_2'],
}

const minimalYaml = `instruction: |
  Do the thing.
model:
  provider_config: "anthropic"
  name: "claude-sonnet-5"
`

function mustDeserialize(source: string): BasicConfig {
  const config = deserializeBasicConfig(source)
  if (config == null) throw new Error('expected the config to deserialize')
  return config
}

describe('deserializeBasicConfig', () => {
  it('round-trips a full builder-authored config', () => {
    const source = serializeBasicConfig(fullConfig)
    const config = mustDeserialize(source)
    expect(serializeBasicConfig(config)).toBe(source)
    expect(config).toMatchObject({
      instruction: fullConfig.instruction,
      providerConfig: 'anthropic',
      modelName: 'claude-sonnet-5',
      skillIds: ['skl_1', 'skl_2'],
    })
    expect(config.machineSources.map((s) => [s.kind, s.name])).toEqual([
      ['pool', 'default-pool'],
      ['machine', 'build-box'],
    ])
    const [poolSource] = config.machineSources
    expect(poolSource).toMatchObject({
      defaultCwd: '/workspace',
      initialNumMachines: '2',
      maxMachines: '5',
      machineCpu: '4',
      machineMemoryMb: '8192',
    })
    expect(poolSource?.envRows.map((row) => [row.key, row.value])).toEqual([['MODE', 'ci']])
    expect(config.tools).toMatchObject([
      { name: 'shell', permission: { mode: 'always_ask', parameters: {} } },
      {
        name: 'browser',
        permission: { mode: 'allowlist', parameters: { hosts: ['example.com'] } },
      },
    ])
    expect(config.mcpServers.map((server) => server.authType)).toEqual(['none', 'sigv4', 'bearer'])
    expect(config.mcpServers[1]).toMatchObject({
      secretId: 'sec_456',
      service: 'execute-api',
      region: 'us-east-1',
    })
  })

  it('round-trips a minimal config', () => {
    expect(serializeBasicConfig(mustDeserialize(minimalYaml))).toBe(minimalYaml)
  })

  it('accepts equivalent YAML with different formatting and comments', () => {
    const reformatted = `# hand-written
instruction: Do the thing.
model: { provider_config: 'anthropic', name: 'claude-sonnet-5' }
`
    expect(serializeBasicConfig(mustDeserialize(reformatted))).toBe(minimalYaml)
  })

  it('accepts and drops a v1 version marker', () => {
    expect(serializeBasicConfig(mustDeserialize(`${minimalYaml}version: v1\n`))).toBe(minimalYaml)
  })

  it('accepts explicitly empty permission parameters', () => {
    const source = `${minimalYaml}tools:
  shell:
    type: built_in
    permission:
      mode: always_ask
      parameters: {}
`
    const config = mustDeserialize(source)
    expect(config.tools).toEqual([
      { name: 'shell', permission: { mode: 'always_ask', parameters: {} } },
    ])
  })

  it('accepts omitted defaults: tool type, enabled, and mcp default_enabled', () => {
    const source = `instruction: >
  Help the user make progress on their local machine.

  Ask questions when you need clarification.
model:
  provider_config: prod-openai
  name: gpt-5.6-sol
tools:
  ask_question:
    enabled: true
    permission:
      mode: always_ask
  run_command:
    permission:
      mode: always_ask
mcp:
  search:
    url: https://mcp.example.com
    permission:
      mode: always_ask
machine_sources:
  - machine_name: Christians-MacBook-Pro.local
    cwd: /Users/csparks/repos/omnara-agents/examples/cli-agent
`
    const config = mustDeserialize(source)
    expect(config.tools.map((tool) => tool.name)).toEqual(['ask_question', 'run_command'])
    expect(config.mcpServers).toMatchObject([{ name: 'search', defaultEnabled: true }])
    expect(config.machineSources).toMatchObject([
      {
        kind: 'machine',
        name: 'Christians-MacBook-Pro.local',
        defaultCwd: '/Users/csparks/repos/omnara-agents/examples/cli-agent',
      },
    ])
  })

  it('rejects disabled tools', () => {
    const source = `${minimalYaml}tools:
  shell:
    enabled: false
    permission:
      mode: always_ask
`
    expect(deserializeBasicConfig(source)).toBeNull()
  })

  it('rejects unknown fields', () => {
    expect(deserializeBasicConfig(`${minimalYaml}unknown_field: 1\n`)).toBeNull()
    expect(
      deserializeBasicConfig(minimalYaml.replace('model:', 'model:\n  extra: true')),
    ).toBeNull()
  })

  it('round-trips a pool provider options overlay and infers its provider', () => {
    const source = `${minimalYaml}machine_sources:
  - machine_pool_name: "default-pool"
    machine_provider_options_overlay: {"snapshot":"base-image","target":"us","startup_script":"echo hi"}
`
    const config = mustDeserialize(source)
    expect(config.machineSources).toMatchObject([
      {
        kind: 'pool',
        name: 'default-pool',
        provider: 'daytona',
        providerOptions: { resource: 'base-image', location: 'us', startupScript: 'echo hi' },
      },
    ])
    expect(serializeBasicConfig(config)).toBe(source)
  })

  it('rejects provider options overlays no provider accounts for', () => {
    const unknownKey = `${minimalYaml}machine_sources:
  - machine_pool_name: "default-pool"
    machine_provider_options_overlay: {"instance_type":"m5.large"}
`
    expect(deserializeBasicConfig(unknownKey)).toBeNull()
    const mixedProviders = `${minimalYaml}machine_sources:
  - machine_pool_name: "default-pool"
    machine_provider_options_overlay: {"metro":"sfo","target":"us"}
`
    expect(deserializeBasicConfig(mixedProviders)).toBeNull()
    const empty = `${minimalYaml}machine_sources:
  - machine_pool_name: "default-pool"
    machine_provider_options_overlay: {}
`
    expect(deserializeBasicConfig(empty)).toBeNull()
  })

  it('rejects non-built-in tools', () => {
    const source = `${minimalYaml}tools:
  shell:
    type: custom
    permission:
      mode: always_ask
`
    expect(deserializeBasicConfig(source)).toBeNull()
  })

  it('rejects invalid and empty YAML', () => {
    expect(deserializeBasicConfig('')).toBeNull()
    expect(deserializeBasicConfig('instruction: [')).toBeNull()
    expect(deserializeBasicConfig('- just\n- a\n- list\n')).toBeNull()
  })
})
