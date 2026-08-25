/** @vitest-environment happy-dom */

import type * as OmnaraReact from '@omnara/react'
import type { ConfiguredModelSummary, ToolCatalog } from '@omnara/sdk'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import {
  afterAll,
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  type Mock,
  vi,
} from 'vitest'

import type { AgentTemplate } from '@/components/agents/agentTemplates'
import { CreateAgentForm } from '@/components/agents/CreateAgentForm'

interface ModelGrantItem {
  grant: { supports_tools?: boolean | null }
  model: ConfiguredModelSummary
}

interface FormViewProps {
  defaultModel?: ConfiguredModelSummary
  templatesReady: boolean
  initialTemplate?: AgentTemplate
}

interface TestFixtures {
  template?: string
  poolPending: boolean
  fallbackGrants?: ModelGrantItem[]
  fallbackPending: boolean
  preferredGrants?: ModelGrantItem[]
  preferredPending: boolean
  formView: Mock<(props: FormViewProps) => void>
}

const fixtures = vi.hoisted<TestFixtures>(() => ({
  template: 'general',
  poolPending: false,
  fallbackGrants: [],
  fallbackPending: false,
  preferredGrants: [],
  preferredPending: false,
  formView: vi.fn(),
}))

const catalog: ToolCatalog = {
  built_in_tools: [],
  custom_tool_permissions: {
    default_permission: { mode: 'always_ask', parameters: {} },
    permission_modes: [],
  },
  mcp_tool_permissions: {
    default_permission: { mode: 'always_ask', parameters: {} },
    permission_modes: [],
  },
}

vi.mock('@omnara/react', async (importOriginal) => {
  const actual = await importOriginal<typeof OmnaraReact>()
  return {
    ...actual,
    useProjectMachinePoolGrants: () => ({
      data: { pages: [{ data: [] }] },
      isPending: fixtures.poolPending,
    }),
    useProjectModelGrants: (
      _orgId: string,
      _projectId: string,
      options?: { filters?: { name?: string }; pageSize?: number },
    ) => {
      const preferred = options?.filters?.name === 'openai/gpt-5.6-sol'
      const grants = preferred ? fixtures.preferredGrants : fixtures.fallbackGrants
      const page =
        grants === undefined || options?.pageSize === undefined
          ? grants
          : grants.slice(0, options.pageSize)
      return {
        data: page === undefined ? undefined : { pages: [{ data: page }] },
        isPending: preferred ? fixtures.preferredPending : fixtures.fallbackPending,
      }
    },
    useToolCatalog: () => ({
      data: catalog,
      error: null,
      isError: false,
      isPending: false,
    }),
  }
})

vi.mock('@tanstack/react-router', () => ({
  useSearch: () => ({ template: fixtures.template }),
}))

vi.mock('@/components/agents/CreateAgentFormView', () => ({
  CreateAgentFormView: (props: FormViewProps) => {
    fixtures.formView(props)
    return <div data-testid="form" />
  },
}))

vi.mock('@/lib/use-project-page', () => ({
  useProjectPage: () => ({
    activeOrg: { id: 'org_a' },
    projectId: 'proj_a',
  }),
}))

let container: HTMLDivElement
let root: Root
let previousActEnvironment: boolean | undefined

function configuredModel(name: string, providerConfig: string): ConfiguredModelSummary {
  return {
    id: `cmod_${name}_${providerConfig}`,
    org_id: 'org_a',
    model_provider_config_id: `mpc_${providerConfig}`,
    name,
    provider_config: providerConfig,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  }
}

function modelGrant(model: ConfiguredModelSummary, supportsTools?: boolean | null): ModelGrantItem {
  return { grant: { supports_tools: supportsTools }, model }
}

function renderForm() {
  act(() => {
    root.render(<CreateAgentForm />)
  })
}

function latestFormProps() {
  const props = fixtures.formView.mock.calls.at(-1)?.[0]
  if (!props) throw new Error('Missing form props')
  return props
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
  fixtures.template = 'general'
  fixtures.poolPending = false
  fixtures.fallbackGrants = []
  fixtures.fallbackPending = false
  fixtures.preferredGrants = []
  fixtures.preferredPending = false
  fixtures.formView.mockClear()
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

describe('CreateAgentForm', () => {
  it('prefers the eligible hosted Sol grant for a linked template', () => {
    const fallback = configuredModel('anthropic/claude-fable-5', 'omnara-openrouter')
    const tenantSol = configuredModel('openai/gpt-5.6-sol', 'customer-openrouter')
    const hostedSol = configuredModel('openai/gpt-5.6-sol', 'omnara-openrouter')
    fixtures.fallbackGrants = [modelGrant(fallback)]
    fixtures.preferredGrants = [modelGrant(tenantSol), modelGrant(hostedSol, true)]

    renderForm()

    expect(latestFormProps()).toMatchObject({
      defaultModel: hostedSol,
      templatesReady: true,
      initialTemplate: { id: 'general' },
    })
  })

  it('falls back when the preferred model is unavailable and skips a rejected hosted Sol', () => {
    const fallback = configuredModel('anthropic/claude-fable-5', 'omnara-openrouter')
    const hostedSol = configuredModel('openai/gpt-5.6-sol', 'omnara-openrouter')
    fixtures.fallbackGrants = [modelGrant(hostedSol, false), modelGrant(fallback)]
    fixtures.preferredGrants = undefined

    renderForm()

    expect(latestFormProps().defaultModel).toBe(fallback)
  })

  it('only blocks unresolved defaults for linked templates', () => {
    fixtures.template = undefined
    fixtures.poolPending = true
    fixtures.fallbackPending = true
    fixtures.preferredPending = true

    renderForm()

    expect(latestFormProps()).toMatchObject({
      templatesReady: false,
      initialTemplate: undefined,
    })

    fixtures.formView.mockClear()
    fixtures.template = 'general'
    renderForm()

    expect(fixtures.formView).not.toHaveBeenCalled()
  })
})
