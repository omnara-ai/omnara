import { describe, expect, it } from 'vitest'
import { parse } from 'yaml'

import { emptyProviderOptions } from '@/components/machines/machineOverrides'

import {
  type BasicConfig,
  basicConfigValid,
  createBasicConfigSession,
  mcpRuntimeToolNameError,
  mcpServerNameError,
  mcpToolEnabled,
} from './useAgentBuilderForm'

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
      deleteAfterIdleMinutes: '0',
      machineCpu: '4',
      machineMemoryGb: '8',
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
      deleteAfterIdleMinutes: '',
      machineCpu: '',
      machineMemoryGb: '',
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
      tools: [],
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
      tools: [],
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
      tools: [],
    },
  ],
  skillIds: ['skl_1', 'skl_2'],
}

const minimalYaml = `instruction: Do the thing.
model:
  provider_config: anthropic
  name: claude-sonnet-5
`

const commentedYaml = `# top comment
version: v1
instruction: |
  Do the thing.
model:
  # which model to use
  provider_config: "anthropic"
  name: "claude-sonnet-5"
unknown_field: keep me
tools:
  # keep shell locked down
  shell:
    permission:
      mode: always_ask
  browser:
    permission:
      mode: always_ask
mcp:
  search: # our search proxy
    url: https://mcp.example.com
    permission:
      mode: always_ask
machine_sources:
  # primary pool
  - machine_pool_name: "default-pool"
    cwd: /workspace
  - machine_name: build-box
`

function deserialize(source: string): BasicConfig | null {
  return createBasicConfigSession(source).initialDraft
}

function mustDeserialize(source: string): BasicConfig {
  const config = createBasicConfigSession(source).initialDraft
  if (config == null) throw new Error('expected the config to deserialize')
  return config
}

function applyToSource(source: string, config: BasicConfig): string {
  return createBasicConfigSession(source).apply(config)
}

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
      deleteAfterIdleMinutes: '0',
      machineCpu: '4',
      machineMemoryGb: '8',
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

  it('treats missing or empty instruction and model fields as blank drafts', () => {
    expect(mustDeserialize('instruction:\nmodel:\n  provider_config: anthropic\n')).toMatchObject({
      instruction: '',
      providerConfig: 'anthropic',
      modelName: '',
    })
    expect(mustDeserialize('model:\n')).toMatchObject({
      instruction: '',
      providerConfig: '',
      modelName: '',
    })
    expect(mustDeserialize('{}')).toMatchObject({
      instruction: '',
      providerConfig: '',
      modelName: '',
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

  it('accepts omitted permissions and round-trips them', () => {
    const source = `${minimalYaml}tools:
  ask_question: {}
mcp:
  search:
    url: https://mcp.example.com
`
    const config = mustDeserialize(source)
    expect(config.tools).toEqual([{ name: 'ask_question', permission: null }])
    expect(config.mcpServers).toMatchObject([{ name: 'search', permission: null }])
    expect(applyToSource(source, config)).toBe(source)
  })

  it('round-trips per-tool mcp overrides and drops empty ones', () => {
    const source = `${minimalYaml}mcp:
  search:
    url: https://mcp.example.com
    tools:
      web_search:
        enabled: false
      fetch:
        permission:
          mode: always_allow
`
    const config = mustDeserialize(source)
    expect(config.mcpServers[0]?.tools).toEqual([
      { name: 'web_search', enabled: false, permission: null },
      { name: 'fetch', enabled: null, permission: { mode: 'always_allow', parameters: {} } },
    ])
    expect(applyToSource(source, config)).toBe(source)

    const [server] = config.mcpServers
    if (server == null) throw new Error('missing server')
    const cleared = applyToSource(source, {
      ...config,
      mcpServers: [
        {
          ...server,
          tools: [
            { name: 'web_search', enabled: null, permission: null },
            { name: 'fetch', enabled: true, permission: null },
          ],
        },
      ],
    })
    expect(parse(cleared)).toEqual({
      ...parse(minimalYaml),
      mcp: {
        search: {
          url: 'https://mcp.example.com',
          default_enabled: true,
          tools: { fetch: { enabled: true } },
        },
      },
    })
  })

  it('accepts zero machine counts and round-trips them', () => {
    const source = `${minimalYaml}machine_sources:
  - machine_pool_name: default-pool
    initial_num_machines: 0
    max_machines: 0
`
    const config = mustDeserialize(source)
    expect(config.machineSources).toMatchObject([{ initialNumMachines: '0', maxMachines: '0' }])
    expect(applyToSource(source, config)).toBe(source)
  })

  it('rejects negative machine counts', () => {
    expect(
      deserialize(`${minimalYaml}machine_sources:
  - machine_pool_name: default-pool
    max_machines: -1
`),
    ).toBeNull()
  })

  it('accepts null overlay values and round-trips them', () => {
    const source = `${minimalYaml}machine_sources:
  - machine_pool_name: default-pool
    env_overlay:
      MODE: null
    secret_env_overlay:
      TOKEN: null
`
    const config = mustDeserialize(source)
    const [pool] = config.machineSources
    expect(pool?.envRows.map((row) => [row.key, row.value])).toEqual([['MODE', null]])
    expect(pool?.secretEnvRows.map((row) => [row.key, row.secretId])).toEqual([['TOKEN', null]])
    expect(applyToSource(source, config)).toBe(source)
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

describe('createBasicConfigSession apply', () => {
  it('keeps an empty source empty for an untouched form', () => {
    const emptyConfig: BasicConfig = {
      instruction: '',
      providerConfig: '',
      modelName: '',
      machineSources: [],
      tools: [],
      mcpServers: [],
      skillIds: [],
    }
    expect(applyToSource('', emptyConfig)).toBe('')
  })

  it('returns the source verbatim when the draft matches it', () => {
    const config = mustDeserialize(commentedYaml)
    expect(applyToSource(commentedYaml, config)).toBe(commentedYaml)
  })

  it('rewrites only the entries that changed, preserving everything else', () => {
    const config = mustDeserialize(commentedYaml)
    const updated = applyToSource(commentedYaml, {
      ...config,
      tools: config.tools.map((tool) =>
        tool.name === 'browser'
          ? { ...tool, permission: { mode: 'always_allow', parameters: {} } }
          : tool,
      ),
    })
    expect(updated).toContain('# top comment')
    expect(updated).toContain('# which model to use')
    expect(updated).toContain('provider_config: "anthropic"')
    expect(updated).toContain('unknown_field: keep me')
    expect(updated).toContain(
      '# keep shell locked down\n  shell:\n    permission:\n      mode: always_ask',
    )
    expect(updated).toContain('# our search proxy')
    expect(updated).toContain('# primary pool')
    expect(parse(updated)).toMatchObject({
      version: 'v1',
      tools: {
        shell: { permission: { mode: 'always_ask' } },
        browser: { type: 'built_in', permission: { mode: 'always_allow' } },
      },
    })
  })

  it('preserves untouched machine source entries when another one changes', () => {
    const config = mustDeserialize(commentedYaml)
    const updated = applyToSource(commentedYaml, {
      ...config,
      machineSources: config.machineSources.map((source) =>
        source.kind === 'machine' ? { ...source, defaultCwd: '/builds' } : source,
      ),
    })
    expect(updated).toContain('# primary pool')
    expect(updated).toContain('machine_pool_name: "default-pool"')
    expect(parse(updated)).toMatchObject({
      machine_sources: [
        { machine_pool_name: 'default-pool', cwd: '/workspace' },
        { machine_name: 'build-box', cwd: '/builds' },
      ],
    })
  })

  it('removes sections whose entries were all removed', () => {
    const config = mustDeserialize(commentedYaml)
    const updated = applyToSource(commentedYaml, { ...config, mcpServers: [] })
    expect(parse(updated)).not.toHaveProperty('mcp')
    expect(updated).toContain('# keep shell locked down')
  })

  it('updates the instruction without disturbing the rest of the document', () => {
    const config = mustDeserialize(commentedYaml)
    const updated = applyToSource(commentedYaml, { ...config, instruction: 'New plan.' })
    expect(updated).toContain('instruction: New plan.')
    expect(updated).toContain('# top comment')
    expect(updated).toContain('# our search proxy')
    expect(mustDeserialize(updated).instruction).toBe('New plan.')
  })

  it('preserves hidden provider overlay values when an unrelated field changes on a cluster pool', () => {
    const source = `${minimalYaml}machine_sources:
  - machine_pool_name: "default-pool"
    machine_provider_options_overlay: {"image":"my-image","region":"us-pdx-1"}
`
    const config = mustDeserialize(source)
    const updated = applyToSource(source, {
      ...config,
      machineSources: config.machineSources.map((row) => ({
        ...row,
        provider: 'blaxel',
        managementKind: 'cluster',
        defaultCwd: '/workspace',
      })),
    })
    expect(parse(updated)).toMatchObject({
      machine_sources: [
        {
          machine_pool_name: 'default-pool',
          cwd: '/workspace',
          machine_provider_options_overlay: { image: 'my-image', region: 'us-pdx-1' },
        },
      ],
    })
  })

  it('does not rewrite a row when only its resolved provider changed', () => {
    const source = `${minimalYaml}machine_sources:
  - machine_pool_name: "default-pool"
    machine_provider_options_overlay: {"image":"my-image"}
`
    const config = mustDeserialize(source)
    const backfilled = config.machineSources.map((row) => ({
      ...row,
      provider: 'blaxel',
      managementKind: 'cluster',
    }))
    expect(applyToSource(source, { ...config, machineSources: backfilled })).toBe(source)
  })

  it('serializes incomplete drafts so the YAML tab can mirror them', () => {
    const config = mustDeserialize(minimalYaml)
    const updated = applyToSource(minimalYaml, {
      ...config,
      mcpServers: [
        {
          id: 'mcp-incomplete',
          name: '',
          url: '',
          permission: { mode: 'always_ask', parameters: {} },
          defaultEnabled: true,
          authType: 'none',
          secretId: '',
          service: '',
          region: '',
          tools: [],
        },
      ],
    })
    expect(parse(updated)).toMatchObject({ mcp: { '': { url: '' } } })
  })

  it('builds a fresh document when there is no baseline source', () => {
    const source = applyToSource('', fullConfig)
    expect(parse(source)).toMatchObject({
      instruction: fullConfig.instruction,
      model: { provider_config: 'anthropic', name: 'claude-sonnet-5' },
      skills: ['skl_1', 'skl_2'],
      tools: { shell: { type: 'built_in' } },
    })
  })
})

describe('basic agent config names', () => {
  it.each([
    ['provider config', { providerConfig: ' anthropic' }],
    ['configured model', { modelName: 'claude-sonnet-5 ' }],
  ])('rejects rather than trims %s references', (_case, patch) => {
    expect(basicConfigValid({ ...fullConfig, ...patch })).toBe(false)
  })

  it('rejects rather than trims machine references', () => {
    const machineSources = fullConfig.machineSources.map((source, index) =>
      index === 0 ? { ...source, name: ` ${source.name}` } : source,
    )
    expect(basicConfigValid({ ...fullConfig, machineSources })).toBe(false)
  })

  it('rejects rather than trims MCP server keys', () => {
    const mcpServers = fullConfig.mcpServers.map((server, index) =>
      index === 0 ? { ...server, name: ` ${server.name}` } : server,
    )
    expect(basicConfigValid({ ...fullConfig, mcpServers })).toBe(false)
  })

  it.each([
    ['', 'Name is required.'],
    [' github', 'Name must start with a letter.'],
    ['1github', 'Name must start with a letter.'],
    ['git_hub', 'Name may only contain letters, numbers, and hyphens.'],
    ['a'.repeat(33), 'Name cannot exceed 32 characters.'],
    ['github', undefined],
    ['GitHub-2', undefined],
  ])('reports the MCP server key rule for %j', (name, expected) => {
    expect(mcpServerNameError(name)).toBe(expected)
  })

  it('explains when the prefixed MCP tool name exceeds the model limit', () => {
    const tool = 'provider__search-call-recordings-by-metadata'
    expect(mcpRuntimeToolNameError('cust-read', tool)).toBeUndefined()
    expect(mcpRuntimeToolNameError('customer-user-read', tool)).toBe(
      `"${tool}" becomes "mcp__customer-user-read__${tool}" (69 characters) once the server name is prefixed, ` +
        'but the model only accepts tool names of 64 characters or fewer. ' +
        'Shorten the server name to 13 characters or fewer.',
    )
    expect(mcpRuntimeToolNameError('a', 'b'.repeat(64))).toBe(
      `"${'b'.repeat(64)}" becomes "mcp__a__${'b'.repeat(64)}" (72 characters) once the server name is prefixed, ` +
        'but the model only accepts tool names of 64 characters or fewer. ' +
        'The tool name itself is too long to expose under any server name.',
    )
  })

  it('resolves whether an MCP tool is enabled from its override or the server default', () => {
    const [enabledByDefault, disabledByDefault] = fullConfig.mcpServers
    if (!enabledByDefault || !disabledByDefault) throw new Error('fixture needs two servers')
    const overrides = [
      { name: 'on', enabled: true, permission: null },
      { name: 'off', enabled: false, permission: null },
      { name: 'inherit', enabled: null, permission: null },
    ]
    const enabled = { ...enabledByDefault, tools: overrides }
    const disabled = { ...disabledByDefault, tools: overrides }
    expect(mcpToolEnabled(enabled, 'on')).toBe(true)
    expect(mcpToolEnabled(enabled, 'off')).toBe(false)
    expect(mcpToolEnabled(enabled, 'inherit')).toBe(true)
    expect(mcpToolEnabled(enabled, 'unknown')).toBe(true)
    expect(mcpToolEnabled(disabled, 'on')).toBe(true)
    expect(mcpToolEnabled(disabled, 'off')).toBe(false)
    expect(mcpToolEnabled(disabled, 'inherit')).toBe(false)
    expect(mcpToolEnabled(disabled, 'unknown')).toBe(false)
  })

  it('rejects duplicate MCP server keys', () => {
    const mcpServers = fullConfig.mcpServers.map((server, index) =>
      index === 1 ? { ...server, name: fullConfig.mcpServers[0]?.name ?? '' } : server,
    )
    expect(basicConfigValid({ ...fullConfig, mcpServers })).toBe(false)
  })

  it('preserves accepted resource names except for NFC canonicalization', () => {
    const config = {
      ...fullConfig,
      providerConfig: 'Production Cafe\u0301',
      modelName: 'Mode\u0301l 5',
      machineSources: fullConfig.machineSources.map((source, index) =>
        index === 0 ? { ...source, name: 'Primary Cafe\u0301  Pool' } : source,
      ),
    }
    const roundTripped = mustDeserialize(applyToSource('', config))
    expect(roundTripped.providerConfig).toBe('Production Café')
    expect(roundTripped.modelName).toBe('Modél 5')
    expect(roundTripped.machineSources[0]?.name).toBe('Primary Café  Pool')
  })
})
