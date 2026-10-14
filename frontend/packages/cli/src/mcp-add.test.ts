import { createOmnaraClient, type JsonBody, schemas } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'
import { parse } from 'yaml'
import * as z from 'zod'

import { runAgentMcpAdd, zMcpAddBody } from './mcp-add.ts'

const id = (prefix: string) => `${prefix}_${'a'.repeat(26)}`
const now = '2026-10-14T00:00:00Z'
const secretId = id('sec')

describe('MCP add names', () => {
  it('uses the resource-name contract for explicit secret names', () => {
    const result = zMcpAddBody.parse({
      mcp_url: 'https://mcp.example.com',
      server_name: 'docs',
      secret_name: 'Café MCP',
    })
    expect(result.secret_name).toBe('Café MCP')
    expect(
      zMcpAddBody.safeParse({
        mcp_url: 'https://mcp.example.com',
        server_name: 'docs',
        secret_name: ' invalid',
      }).success,
    ).toBe(false)
  })
})

it('adds the server to the generated source when the config has no saved source', async () => {
  const path = { orgID: id('org'), projectID: id('proj'), agentID: id('agt') }
  const configId = id('acfg')
  const agent = {
    id: path.agentID,
    org_id: path.orgID,
    project_id: path.projectID,
    state: 'active',
    name: 'launched',
    current_config_id: configId,
    created_at: now,
    updated_at: now,
  }
  const config = {
    id: configId,
    org_id: path.orgID,
    project_id: path.projectID,
    effective_definition_hash: 'hash',
    created_at: now,
    model: {
      provider_config: 'openai',
      name: 'gpt',
      provider_model_slug: 'gpt',
      configured_model_id: id('mdl'),
      current_revision_id: id('mrev'),
      api_format: 'openai-responses',
      api_variant: 'openai',
      context_window_tokens: 128_000,
      max_output_tokens: null,
      default_cache_retention: 'none',
      supports_tools: true,
      supports_reasoning: false,
      default_reasoning_effort: '',
      supported_reasoning_efforts: [],
      input_modalities: [],
      output_modalities: [],
    },
    compiled_definition: { instruction: 'Help.', model: { configured_model_id: id('mdl') } },
  }
  const json = (value: JsonBody, status = 200) =>
    new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
  let update: unknown
  const client = createOmnaraClient({
    baseUrl: 'https://omnara.test/api/v1',
    fetch: async (input, init) => {
      const request = new Request(input, init)
      const pathname = new URL(request.url).pathname
      if (pathname.endsWith('/secrets/mcp-oauth')) {
        return json({
          flow_id: id('moaf'),
          authorization_url: 'https://mcp.example.com/authorize',
          expires_at: '2999-01-01T00:00:00Z',
        })
      }
      if (pathname.endsWith('/secrets')) {
        return json({
          data: [
            {
              id: secretId,
              org_id: path.orgID,
              management_kind: 'tenant',
              owner: { kind: 'project', project_id: path.projectID },
              name: 'docs-mcp',
              kind: 'generic',
              metadata: {},
              current_version_number: 1,
              payload_keys: [],
              created_at: now,
              updated_at: now,
            },
          ],
          next_cursor: null,
        })
      }
      if (request.method === 'POST') {
        update = await request.json()
        return json({ code: 'conflict', error: 'stop after capturing the update' }, 409)
      }
      if (pathname.endsWith('/generated-source')) return json({ source: 'instruction: Help.\n' })
      if (pathname.endsWith(`/agent-configs/${configId}`)) return json(config)
      return json({ agent, machine_ids: [], mcp_connections: [] })
    },
  })
  const report = {
    url: vi.fn(),
    start: vi.fn(),
    stop: vi.fn(),
    fail: vi.fn(),
    warn: vi.fn(),
    info: vi.fn(),
    done: vi.fn(),
  }
  await expect(
    runAgentMcpAdd({
      client,
      apiUrl: 'https://omnara.test/api/v1',
      path,
      report,
      body: zMcpAddBody.parse({
        mcp_url: 'https://mcp.example.com',
        server_name: 'docs',
        browser: false,
      }),
    }),
  ).rejects.toThrow()
  const body = schemas.zUpdateAgentConfigRequest.parse(update)
  expect(body).toMatchObject({ source_format: 'yaml', expected_current_config_id: configId })
  expect(parse(z.string().parse(body.source))).toEqual({
    instruction: 'Help.',
    mcp: { docs: { url: 'https://mcp.example.com', auth: { type: 'oauth', secret_id: secretId } } },
  })
})
