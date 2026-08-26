/** @vitest-environment happy-dom */

import type {
  Agent,
  AgentProfile,
  OrgOverviewResponse,
  PersonalAccessToken,
  VisibleProject,
} from '@omnara/sdk'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { AgentOnboarding } from '@/components/overview/AgentOnboarding'
import { cliLoginCommand, cliSetupPrompt } from '@/components/overview/onboardingCli'

const hooks = vi.hoisted(() => ({
  orgOverview: vi.fn<(orgId: string, options: { refetchInterval?: number | false }) => void>(),
  personalAccessTokens:
    vi.fn<(pageSize: number, options: { refetchInterval?: number | false }) => void>(),
  tokens: [] as PersonalAccessToken[],
  createAgentConfig: vi.fn(),
  createAgentProfile: vi.fn(),
  createAgent: vi.fn(),
}))

vi.mock('@omnara/react', () => ({
  useOrgOverview: (orgId: string, options: { refetchInterval?: number | false }) => {
    hooks.orgOverview(orgId, options)
  },
  usePersonalAccessTokens: (pageSize: number, options: { refetchInterval?: number | false }) => {
    hooks.personalAccessTokens(pageSize, options)
    return { data: { pages: [{ data: hooks.tokens }] } }
  },
  useToolCatalog: () => ({ isPending: false, data: { built_in_tools: [] } }),
  useProjectMachinePoolGrants: () => ({ isPending: false, data: { pages: [{ data: [] }] } }),
  useProjectModelGrants: () => ({
    isPending: false,
    data: {
      pages: [{ data: [{ model: { name: 'claude-fable-5', provider_config: 'openrouter' } }] }],
    },
  }),
  useCreateAgentConfig: () => ({ mutateAsync: hooks.createAgentConfig }),
  useCreateAgentProfile: () => ({ mutateAsync: hooks.createAgentProfile }),
  useCreateAgent: () => ({ mutateAsync: hooks.createAgent, isPending: false }),
}))

vi.mock('@/components/agents/AgentConfigBasicForm', () => ({
  AgentConfigBasicForm: () => <div data-slot="basic-form" />,
}))

vi.mock('@/components/agents/AgentConfigModelField', () => ({
  AgentConfigModelField: () => <div data-slot="model-field" />,
}))

vi.mock('@/lib/web-config', () => ({
  useWebConfig: () => ({ data: undefined }),
}))

vi.mock('@tanstack/react-router', () => ({
  Link: ({
    to,
    params,
    children,
    ...props
  }: {
    to: string
    params?: Record<string, string>
    children: React.ReactNode
  }) => {
    let href = to
    for (const [key, value] of Object.entries(params ?? {})) href = href.replace(`$${key}`, value)
    return (
      <a href={href} {...props}>
        {children}
      </a>
    )
  },
}))

const project: VisibleProject = {
  id: 'proj-1',
  org_id: 'org-1',
  name: 'Default project',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  access: { can_read: true, can_manage: true, can_manage_access: true, can_operate: true },
}

function tokenFixture(overrides: Partial<PersonalAccessToken> = {}): PersonalAccessToken {
  return {
    id: 'pat-1',
    user_id: 'user-1',
    name: 'Omnara CLI on laptop',
    token_id: 'omn_abc',
    created_at: '2026-01-01T00:00:00Z',
    last_used_at: null,
    revoked_at: null,
    ...overrides,
  }
}

function profileFixture(overrides: Partial<AgentProfile> = {}): AgentProfile {
  return {
    id: 'profile-1',
    org_id: 'org-1',
    project_id: project.id,
    name: 'General agent',
    current_config_id: 'config-1',
    current_generation: 1,
    current_config: {
      id: 'config-1',
      org_id: 'org-1',
      project_id: project.id,
      effective_definition_hash: 'hash',
      model: {
        provider_config: 'openrouter',
        name: 'claude-fable-5',
        provider_model_slug: 'anthropic/claude-fable-5',
        configured_model_id: 'model-1',
        current_revision_id: 'revision-1',
        api_format: 'openai-chat-completions',
        api_variant: 'openai_chat_completions',
        context_window_tokens: 200000,
        max_output_tokens: 32000,
        default_cache_retention: 'none',
        supports_tools: true,
        supports_reasoning: false,
        default_reasoning_effort: '',
        supported_reasoning_efforts: [],
        input_modalities: ['text'],
        output_modalities: ['text'],
      },
      created_at: '2026-01-01T00:00:00Z',
    },
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

function agentFixture(overrides: Partial<Agent> = {}): Agent {
  return {
    id: 'agent-1',
    org_id: 'org-1',
    project_id: project.id,
    agent_profile_id: 'profile-1',
    state: 'active',
    name: 'General agent',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

function overviewFixture(overrides: Partial<OrgOverviewResponse> = {}): OrgOverviewResponse {
  return { projects: [project], recent_agents: [], recent_agent_profiles: [], ...overrides }
}

let container: HTMLDivElement
let root: Root
let previousActEnvironment: boolean | undefined

function element(selector: string): HTMLElement {
  const match = container.querySelector(selector)
  if (!(match instanceof HTMLElement)) throw new Error(`Missing test element: ${selector}`)
  return match
}

function render(overview: OrgOverviewResponse, onProfileSeen = vi.fn()) {
  act(() => {
    root.render(
      <AgentOnboarding
        orgId="org-1"
        project={project}
        overview={overview}
        onProfileSeen={onProfileSeen}
      />,
    )
  })
  return onProfileSeen
}

function selectTab(name: 'cli' | 'browser' | 'command' | 'prompt' | 'sdk') {
  act(() => {
    const trigger = element(`[role="tab"][id$="-${name}"]`)
    trigger.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 }))
  })
}

async function click(selector: string) {
  await act(async () => {
    element(selector).click()
    await Promise.resolve()
  })
}

function setInputValue(selector: string, value: string) {
  const input = element(selector)
  if (!(input instanceof HTMLInputElement)) throw new Error(`Not an input: ${selector}`)
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.bind(
    input,
  )
  if (!setValue) throw new Error('Missing HTMLInputElement value setter')
  act(() => {
    setValue(value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

function stepStatus(index: number) {
  return element(`[data-step="${index}"]`).dataset.status
}

beforeAll(() => {
  const actEnvironment = globalThis as typeof globalThis & {
    IS_REACT_ACT_ENVIRONMENT?: boolean
  }
  previousActEnvironment = actEnvironment.IS_REACT_ACT_ENVIRONMENT
  actEnvironment.IS_REACT_ACT_ENVIRONMENT = true
})

afterAll(() => {
  const actEnvironment = globalThis as typeof globalThis & {
    IS_REACT_ACT_ENVIRONMENT?: boolean
  }
  actEnvironment.IS_REACT_ACT_ENVIRONMENT = previousActEnvironment
})

beforeEach(() => {
  hooks.orgOverview.mockReset()
  hooks.personalAccessTokens.mockReset()
  hooks.tokens = []
  hooks.createAgentConfig.mockReset()
  hooks.createAgentProfile.mockReset()
  hooks.createAgent.mockReset()

  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
})

describe('AgentOnboarding CLI tab', () => {
  it('waits for a CLI token on step one and polls tokens and the overview', () => {
    const onProfileSeen = render(overviewFixture())

    expect(onProfileSeen).not.toHaveBeenCalled()
    expect(container.textContent).toContain(cliLoginCommand)
    expect(container.textContent).not.toContain(cliSetupPrompt)
    expect(stepStatus(1)).toBe('active')
    expect(stepStatus(2)).toBe('upcoming')
    expect(stepStatus(3)).toBe('upcoming')
    expect(hooks.personalAccessTokens).toHaveBeenLastCalledWith(50, { refetchInterval: 3000 })
    expect(hooks.orgOverview).toHaveBeenLastCalledWith('org-1', { refetchInterval: 3000 })
  })

  it('completes step one once a CLI login token exists', async () => {
    hooks.tokens = [tokenFixture({ name: 'Manual token' }), tokenFixture()]
    render(overviewFixture())

    expect(stepStatus(1)).toBe('done')
    expect(element('[data-slot="step-completion"]').textContent).toBe('laptop')
    expect(stepStatus(2)).toBe('active')
    const shown = container.textContent.replaceAll(/ \\\n\s*/g, ' ')
    expect(shown).toContain(
      'npx omnara profiles create --org org-1 --project proj-1 --name "General agent" --source',
    )
    expect(shown).toContain('{ … }')
    expect(shown).not.toContain('provider_config')
    expect(element('[aria-label="Copy cli"]')).toBeDefined()

    await click('[data-slot="json-toggle"]')

    expect(container.textContent).toContain('"provider_config": "openrouter"')
    expect(container.textContent).not.toContain(cliSetupPrompt)

    selectTab('prompt')

    expect(container.textContent).toContain(cliSetupPrompt)
    expect(container.querySelector('[data-action="run"]')).toBeNull()

    selectTab('command')
    hooks.createAgentConfig.mockResolvedValue({ id: 'config-3' })
    hooks.createAgentProfile.mockResolvedValue(profileFixture({ id: 'profile-3' }))
    await click('[data-step="2"] [data-action="run"]')

    expect(hooks.createAgentConfig.mock.calls[0]?.[0]).toMatchObject({ source_format: 'json' })
    expect(hooks.createAgentProfile).toHaveBeenCalledWith({
      name: 'General agent',
      config: 'config-3',
    })
    expect(stepStatus(2)).toBe('done')
    expect(stepStatus(3)).toBe('active')
  })

  it('ignores revoked CLI tokens', () => {
    hooks.tokens = [tokenFixture({ revoked_at: '2026-01-02T00:00:00Z' })]
    render(overviewFixture())

    expect(stepStatus(1)).toBe('active')
  })

  it('waits for a chat once the profile exists, pins onboarding, and stops polling tokens', async () => {
    const onProfileSeen = render(overviewFixture({ recent_agent_profiles: [profileFixture()] }))

    expect(onProfileSeen).toHaveBeenCalledWith(true)
    expect(stepStatus(1)).toBe('done')
    expect(stepStatus(2)).toBe('done')
    expect(stepStatus(3)).toBe('active')
    expect(element('[data-step="3"]').textContent.replaceAll(/ \\\n\s*/g, ' ')).toContain(
      'npx omnara agents launch --org org-1 --project proj-1 --profile profile-1 --config config-1',
    )

    expect(hooks.personalAccessTokens).toHaveBeenLastCalledWith(50, { refetchInterval: false })
    expect(hooks.orgOverview).toHaveBeenLastCalledWith('org-1', { refetchInterval: 3000 })

    hooks.createAgent.mockResolvedValue({ agent: agentFixture({ id: 'agent-10' }) })
    await click('[data-step="3"] [data-action="run"]')

    expect(hooks.createAgent).toHaveBeenCalledWith({
      profile: 'profile-1',
      config: 'config-1',
      message: 'Hi! What can you do?',
    })
    expect(stepStatus(3)).toBe('done')
    expect(container.querySelector('[data-step="3"] [data-action="run"]')).toBeNull()
    expect(hooks.orgOverview).toHaveBeenLastCalledWith('org-1', { refetchInterval: false })
  })

  it('shows the chat guide once the agent exists and stops polling', () => {
    render(
      overviewFixture({
        recent_agent_profiles: [profileFixture()],
        recent_agents: [agentFixture({ id: 'agent-9' })],
      }),
    )

    expect(stepStatus(3)).toBe('done')
    expect(element('[data-action="open-agent"]').getAttribute('href')).toBe(
      '/projects/proj-1/agents/agent-9',
    )
    expect(container.textContent.replaceAll(/ \\\n\s*/g, ' ')).toContain(
      'npx omnara agents launch --org org-1 --project proj-1 --profile profile-1',
    )

    selectTab('sdk')

    expect(container.textContent).toContain('sdk.createAgent(')

    expect(container.querySelector('[data-step="3"] [data-action="run"]')).toBeNull()
    expect(hooks.orgOverview).toHaveBeenLastCalledWith('org-1', { refetchInterval: false })
  })
})

describe('AgentOnboarding browser tab', () => {
  it('creates a profile from the selected template, then a chat, then shows the guide', async () => {
    hooks.createAgentConfig.mockResolvedValue({ id: 'config-7' })
    hooks.createAgentProfile.mockResolvedValue(
      profileFixture({ id: 'profile-7', name: 'Deep researcher', current_config_id: 'config-7' }),
    )
    hooks.createAgent.mockResolvedValue({ agent: agentFixture({ id: 'agent-7' }) })
    render(overviewFixture())
    selectTab('browser')

    expect(hooks.personalAccessTokens).toHaveBeenLastCalledWith(50, { refetchInterval: false })
    expect(element('[data-template="general"]').getAttribute('aria-checked')).toBe('true')
    expect(stepStatus(1)).toBe('active')
    expect(stepStatus(2)).toBe('upcoming')

    await click('[data-template="deep-researcher"]')
    await click('[data-action="create-profile"]')

    expect(hooks.createAgentConfig.mock.calls[0]?.[0]).toMatchObject({ source_format: 'yaml' })
    expect(hooks.createAgentProfile).toHaveBeenCalledWith({
      name: 'Deep researcher',
      config: 'config-7',
    })
    expect(stepStatus(1)).toBe('done')
    expect(stepStatus(2)).toBe('active')

    await click('[data-action="create-chat"]')

    expect(hooks.createAgent).toHaveBeenCalledWith({ profile: 'profile-7', config: 'config-7' })
    expect(stepStatus(2)).toBe('done')
    expect(element('[data-action="open-agent"]').getAttribute('href')).toBe(
      '/projects/proj-1/agents/agent-7',
    )
  })

  it('opens the builder inline and returns to the step with the customized draft', async () => {
    hooks.createAgentConfig.mockResolvedValue({ id: 'config-8' })
    hooks.createAgentProfile.mockResolvedValue(profileFixture({ id: 'profile-8', name: 'Ops bot' }))
    render(overviewFixture())
    selectTab('browser')

    await click('[data-action="customize"]')

    expect(element('[data-slot="inline-builder"]')).toBeDefined()
    expect(element('[data-slot="basic-form"]')).toBeDefined()
    expect(container.querySelector('[data-action="create-profile"]')).toBeNull()
    expect(container.querySelectorAll('[data-slot="inline-builder"] button').length).toBe(1)

    setInputValue('#onboarding-profile-name', 'Ops bot')
    await click('[data-action="builder-ok"]')

    expect(container.querySelector('[data-slot="inline-builder"]')).toBeNull()
    expect(element('[data-slot="customized-note"]').textContent).toContain('Ops bot')

    await click('[data-action="create-profile"]')

    expect(hooks.createAgentProfile).toHaveBeenCalledWith({ name: 'Ops bot', config: 'config-8' })
    expect(stepStatus(1)).toBe('done')
  })

  it('shows the profile creation error and stays on step one', async () => {
    hooks.createAgentConfig.mockRejectedValue(new Error('config invalid'))
    render(overviewFixture())
    selectTab('browser')

    await click('[data-action="create-profile"]')

    expect(hooks.createAgentProfile).not.toHaveBeenCalled()
    expect(stepStatus(1)).toBe('active')
    expect(container.textContent).toContain('Could not create profile')
  })

  it('starts on step two when a profile already exists in the project', () => {
    render(overviewFixture({ recent_agent_profiles: [profileFixture()] }))
    selectTab('browser')

    expect(stepStatus(1)).toBe('done')
    expect(stepStatus(2)).toBe('active')
    expect(element('[data-action="create-chat"]')).toBeDefined()
  })
})
