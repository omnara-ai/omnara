import type {
  AgentConfigModel,
  CurrentUser,
  CurrentUserOrg,
  MachinePool,
  OrgInvitation,
  ProjectMachinePoolGrant,
} from '@omnara/sdk'

const timestamp = '2026-01-01T00:00:00Z'

export function fakeId(prefix: string): string {
  return `${prefix}_${'a'.repeat(26)}`
}

export function machinePool(overrides: Partial<MachinePool> = {}): MachinePool {
  return {
    id: fakeId('mpo'),
    org_id: fakeId('org'),
    name: 'pool',
    management_kind: 'tenant',
    description: '',
    provider: 'unikraft',
    default_machine_cpu: null,
    default_machine_memory_mb: null,
    default_machine_env: {},
    default_machine_secret_env: {},
    default_machine_provider_options: {},
    default_cwd: '',
    provider_config: {},
    runtime_protection_enabled: false,
    max_total_machines: 1,
    max_total_cpu: null,
    max_total_memory_mb: null,
    min_machine_cpu: null,
    min_machine_memory_mb: null,
    max_machine_cpu: null,
    max_machine_memory_mb: null,
    delete_after_idle_minutes: null,
    metadata: {},
    created_at: timestamp,
    updated_at: timestamp,
    ...overrides,
  }
}

export function projectMachinePoolGrant(
  overrides: Partial<ProjectMachinePoolGrant> = {},
): ProjectMachinePoolGrant {
  return {
    id: fakeId('pmpg'),
    org_id: fakeId('org'),
    project_id: fakeId('proj'),
    machine_pool_id: fakeId('mpo'),
    description: '',
    default_machine_cpu: null,
    default_machine_memory_mb: null,
    default_machine_env_overlay: {},
    default_machine_secret_env_overlay: {},
    default_machine_provider_options_overlay: {},
    default_cwd: '',
    max_total_machines: null,
    max_total_cpu: null,
    max_total_memory_mb: null,
    min_machine_cpu: null,
    min_machine_memory_mb: null,
    max_machine_cpu: null,
    max_machine_memory_mb: null,
    delete_after_idle_minutes: null,
    metadata: {},
    created_at: timestamp,
    updated_at: timestamp,
    ...overrides,
  }
}

export function agentConfigModel(overrides: Partial<AgentConfigModel> = {}): AgentConfigModel {
  return {
    provider_config: 'openai',
    name: 'gpt',
    provider_model_slug: 'gpt',
    configured_model_id: fakeId('mdl'),
    current_revision_id: fakeId('mrev'),
    api_format: 'openai-responses',
    api_variant: 'openai',
    context_window_tokens: 128_000,
    max_output_tokens: 8_192,
    default_cache_retention: 'none',
    supports_tools: true,
    supports_reasoning: false,
    default_reasoning_effort: '',
    supported_reasoning_efforts: [],
    input_modalities: [],
    output_modalities: [],
    ...overrides,
  }
}

export function currentUserOrg(overrides: Partial<CurrentUserOrg> = {}): CurrentUserOrg {
  return { id: fakeId('org'), name: 'Org 1', role: 'owner', created_at: timestamp, ...overrides }
}

export function currentUser(orgs: CurrentUserOrg[]): CurrentUser {
  return {
    user: { id: fakeId('usr'), email: 'person@example.com', display_name: 'Person' },
    orgs,
  }
}

export function orgInvitation(overrides: Partial<OrgInvitation> = {}): OrgInvitation {
  return {
    id: fakeId('oinv'),
    org_id: fakeId('org'),
    org_name: 'Acme Inc.',
    email: 'person@example.com',
    org_role: 'member',
    created_at: timestamp,
    ...overrides,
  }
}
