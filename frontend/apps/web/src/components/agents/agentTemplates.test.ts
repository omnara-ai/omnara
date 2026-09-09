import type { ToolCatalog, ToolCatalogEntry } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { agentTemplateConfig, agentTemplates, defaultAgentTools } from './agentTemplates'

function catalogEntry(name: string, mode: string): ToolCatalogEntry {
  return {
    name,
    description: `${name} description`,
    default_permission: { mode, parameters: {} },
    permission_modes: [],
  }
}

describe('defaultAgentTools', () => {
  it('uses file tools in templates even when legacy aliases are available', () => {
    const permission = {
      default_permission: { mode: 'always_allow', parameters: {} },
      permission_modes: [],
    }
    const catalog: ToolCatalog = {
      built_in_tools: ['upload_file', 'download_file', 'upload_artifact', 'download_artifact'].map(
        (name) => catalogEntry(name, 'always_allow'),
      ),
      custom_tool_permissions: permission,
      mcp_tool_permissions: permission,
    }

    for (const template of agentTemplates) {
      expect(agentTemplateConfig(template, catalog).tools.map((tool) => tool.name)).toEqual([
        'upload_file',
        'download_file',
      ])
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
