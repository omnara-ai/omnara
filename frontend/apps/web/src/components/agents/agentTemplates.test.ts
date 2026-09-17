import type { ToolCatalog, ToolCatalogEntry } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { machinePool } from '@/test/fixtures'

import { agentTemplateBasicConfig, agentTemplates, defaultAgentTools } from './agentTemplates'

function catalogEntry(name: string, mode: string): ToolCatalogEntry {
  return {
    name,
    description: `${name} description`,
    configurable: true,
    default_permission: { mode, parameters: {} },
    permission_modes: [],
  }
}

describe('defaultAgentTools', () => {
  it('leaves file tools to source defaulting', () => {
    const permission = {
      default_permission: { mode: 'always_allow', parameters: {} },
      permission_modes: [],
    }
    const catalog: ToolCatalog = {
      built_in_tools: ['upload_file', 'download_file'].map((name) =>
        catalogEntry(name, 'always_allow'),
      ),
      custom_tool_permissions: permission,
      mcp_tool_permissions: permission,
    }

    for (const template of agentTemplates) {
      expect(agentTemplateBasicConfig(template, catalog).tools).toEqual([])
    }
  })

  it('selects default tools in order with catalog permissions', () => {
    const webFetch = catalogEntry('web_fetch', 'always_allow')
    const webSearch = catalogEntry('web_search', 'always_ask')
    const catalog: ToolCatalog = {
      built_in_tools: [catalogEntry('ask_question', 'always_allow'), webFetch, webSearch],
      custom_tool_permissions: {
        default_permission: { mode: 'always_ask', parameters: {} },
        permission_modes: [],
      },
      mcp_tool_permissions: {
        default_permission: { mode: 'always_ask', parameters: {} },
        permission_modes: [],
      },
    }

    const tools = defaultAgentTools(catalog)

    expect(tools).toEqual([
      { name: 'web_search', permission: webSearch.default_permission },
      { name: 'web_fetch', permission: webFetch.default_permission },
    ])
    expect(tools[0]?.permission).not.toBe(webSearch.default_permission)
    expect(tools[1]?.permission).not.toBe(webFetch.default_permission)
  })
})

it.each(agentTemplates)(
  '$name leaves machine tools to source defaulting with or without a pool',
  (template) => {
    const names = ['ask_question', 'web_search', 'web_fetch']
    const permissions = {
      default_permission: { mode: 'always_allow', parameters: {} },
      permission_modes: [],
    }
    const catalog: ToolCatalog = {
      built_in_tools: [...names, 'run_command', 'create_machine', 'skill', 'spawn_agent'].map(
        (name) => catalogEntry(name, 'always_allow'),
      ),
      custom_tool_permissions: permissions,
      mcp_tool_permissions: permissions,
    }
    for (const pool of [undefined, machinePool({ management_kind: 'cluster' })]) {
      const config = agentTemplateBasicConfig(template, catalog, pool)
      expect(config.tools.map((tool) => tool.name)).toEqual(names)
      expect(config.machineSources).toHaveLength(pool ? 1 : 0)
      if (pool) expect(config.machineSources[0]?.name).toBe(pool.name)
    }
  },
)
