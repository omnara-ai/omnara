import { describe, expect, it } from 'vitest'

import {
  applyToSource,
  deserialize,
  fullConfig,
  minimalYaml,
  mustDeserialize,
} from '@/components/agents/agentConfigBasicYaml.fixtures'

describe('createBasicConfigSession initialDraft', () => {
  it('round-trips a full builder-authored config', () => {
    const source = applyToSource('', fullConfig)
    const config = mustDeserialize(source)
    expect(applyToSource(source, config)).toBe(source)
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

  it('accepts equivalent YAML with different formatting and comments', () => {
    const reformatted = `# hand-written
instruction: Do the thing.
model: { provider_config: 'anthropic', name: 'claude-sonnet-5' }
`
    const config = mustDeserialize(reformatted)
    expect(config).toMatchObject({
      instruction: 'Do the thing.',
      providerConfig: 'anthropic',
      modelName: 'claude-sonnet-5',
    })
  })

  it('accepts a v1 version marker', () => {
    expect(mustDeserialize(`${minimalYaml}version: v1\n`).instruction).toBe('Do the thing.')
    expect(deserialize(`${minimalYaml}version: v2\n`)).toBeNull()
  })

  it('accepts unknown fields outside builder-owned entries', () => {
    expect(deserialize(`${minimalYaml}unknown_field: 1\n`)).not.toBeNull()
    expect(deserialize(minimalYaml.replace('model:', 'model:\n  extra: true'))).not.toBeNull()
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
    expect(deserialize(source)).toBeNull()
  })

  it('rejects unknown fields inside builder-owned entries', () => {
    expect(
      deserialize(`${minimalYaml}tools:
  shell:
    custom_field: 1
    permission:
      mode: always_ask
`),
    ).toBeNull()
    expect(
      deserialize(`${minimalYaml}tools:
  shell:
    permission:
      mode: always_ask
      extra: true
`),
    ).toBeNull()
    expect(
      deserialize(`${minimalYaml}mcp:
  search:
    url: https://mcp.example.com
    retries: 3
    permission:
      mode: always_ask
`),
    ).toBeNull()
    expect(
      deserialize(`${minimalYaml}machine_sources:
  - machine_name: build-box
    zone: us-east-1
`),
    ).toBeNull()
    expect(
      deserialize(`${minimalYaml}machine_sources:
  - machine_name: build-box
    machine_pool_name: default-pool
`),
    ).toBeNull()
    expect(
      deserialize(`${minimalYaml}machine_sources:
  - machine_name: build-box
    initial_num_machines: 2
`),
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
    expect(applyToSource(source, config)).toBe(source)
  })

  it('rejects provider options overlays no provider accounts for', () => {
    const unknownKey = `${minimalYaml}machine_sources:
  - machine_pool_name: "default-pool"
    machine_provider_options_overlay: {"instance_type":"m5.large"}
`
    expect(deserialize(unknownKey)).toBeNull()
    const mixedProviders = `${minimalYaml}machine_sources:
  - machine_pool_name: "default-pool"
    machine_provider_options_overlay: {"metro":"sfo","target":"us"}
`
    expect(deserialize(mixedProviders)).toBeNull()
    const empty = `${minimalYaml}machine_sources:
  - machine_pool_name: "default-pool"
    machine_provider_options_overlay: {}
`
    expect(deserialize(empty)).toBeNull()
  })

  it('rejects non-built-in tools', () => {
    const source = `${minimalYaml}tools:
  shell:
    type: custom
    permission:
      mode: always_ask
`
    expect(deserialize(source)).toBeNull()
  })

  it('rejects invalid and empty YAML', () => {
    expect(deserialize('')).toBeNull()
    expect(deserialize('instruction: [')).toBeNull()
    expect(deserialize('- just\n- a\n- list\n')).toBeNull()
  })
})
