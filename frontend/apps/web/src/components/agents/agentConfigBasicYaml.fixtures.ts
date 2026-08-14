import { createBasicConfigSession } from '@/components/agents/agentConfigBasicYaml'
import type { BasicConfig } from '@/components/agents/useAgentBuilderForm'
import { emptyProviderOptions } from '@/components/machines/machineOverrides'

export const fullConfig: BasicConfig = {
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

export const minimalYaml = `instruction: Do the thing.
model:
  provider_config: anthropic
  name: claude-sonnet-5
`

export const commentedYaml = `# top comment
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

export function deserialize(source: string): BasicConfig | null {
  return createBasicConfigSession(source).initialDraft
}

export function mustDeserialize(source: string): BasicConfig {
  const config = createBasicConfigSession(source).initialDraft
  if (config == null) throw new Error('expected the config to deserialize')
  return config
}

export function applyToSource(source: string, config: BasicConfig): string {
  return createBasicConfigSession(source).apply(config)
}
