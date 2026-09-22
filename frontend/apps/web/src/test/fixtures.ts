import type {
  AgentConfigModel,
  AppDefinition,
  CurrentUser,
  CurrentUserOrg,
  MachinePool,
  OrgInvitation,
  ProjectApp,
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

export function appDefinition(appType: AppDefinition['app_type'] = 'slack_thread'): AppDefinition {
  const capability = {
    input_schema: { type: 'object', properties: {} },
  }
  const definition: AppDefinition = {
    app_type: appType,
    capabilities: {
      tools:
        appType === 'github_pr'
          ? {
              read: capability,
              discussion_comment: capability,
              inline_comment: capability,
              reply: capability,
            }
          : { read: capability, post_message: capability },
      subscriptions: {
        [appType === 'github_pr' ? 'pull_request' : 'thread_messages']: {
          conversation_schema: { type: 'object', properties: {} },
          events:
            appType === 'github_pr'
              ? ['discussion_comment', 'review_comment', 'commit']
              : ['message'],
        },
      },
    },
  }
  if (appType !== 'github_pr') {
    definition.capabilities.interaction_handler = capability
    definition.capabilities.schedule = {
      description: 'Start a fresh agent in a new channel thread on each run.',
      input_schema: {
        type: 'object',
        additionalProperties: false,
        required: [
          'agent_profile_id',
          'channel_id',
          'opening_message_template',
          'message_template',
        ],
        'x-omnara-field-order': [
          'agent_profile_id',
          'channel_id',
          'opening_message_template',
          'message_template',
        ],
        properties: {
          message_template: {
            type: 'string',
            title: 'Task instructions',
            minLength: 1,
            description: 'The initial task for each new agent.',
            'x-omnara-control': 'textarea',
          },
          channel_id: {
            type: 'string',
            title: 'Channel ID',
            pattern: appType === 'slack_thread' ? '^[CG][A-Z0-9]+$' : '^[1-9][0-9]*$',
            description:
              appType === 'slack_thread'
                ? 'Use a Slack channel ID beginning with C or G.'
                : 'Use a Discord text or announcement channel ID.',
          },
          agent_profile_id: {
            type: 'string',
            title: 'Agent profile',
            pattern: '^aprf_[a-z0-9]{26}$',
            description: 'Choose a profile for future runs.',
            'x-omnara-control': 'agent_profile',
          },
          opening_message_template: {
            type: 'string',
            title: 'Opening message',
            minLength: 1,
            maxLength: 2000,
            default: '{{.trigger.name}} — {{.trigger.local_date}}',
            description:
              'Posted before the agent starts. {{.trigger.local_date}} uses the schedule’s timezone.',
            'x-omnara-control': 'textarea',
          },
        },
      },
    }
  }
  return definition
}

export function projectApp(overrides: Partial<ProjectApp> = {}): ProjectApp {
  const definition = appDefinition(overrides.app_type)
  return {
    id: fakeId('app'),
    project_id: fakeId('proj'),
    name: 'engineering',
    app_type: definition.app_type,
    state: 'disconnected',
    setup_revision: 1,
    settings: {},
    provider_tenant_id: '',
    provider_account_ref: '',
    provider_agent_display_name: '',
    provider_config: {},
    capabilities: definition.capabilities,
    created_at: timestamp,
    updated_at: timestamp,
    ...overrides,
  }
}
