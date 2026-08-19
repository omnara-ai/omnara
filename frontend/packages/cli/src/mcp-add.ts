import {
  type AgentConfig,
  type McpoAuthStartRequest,
  type OmnaraClient,
  sdk,
  type Secret,
} from '@omnara/sdk'
import { zMcpoAuthStartRequest } from '@omnara/sdk/zod'
import * as z from 'zod'

import type { FlowContext } from './factory.ts'
import {
  authorizeMcpOAuthSecret,
  openAuthorizationUrl,
  zBrowserFlag,
} from './mcp-oauth.ts'
import { CliInputError } from './output.ts'
import type { FlowReporter } from './reporter.ts'

type McpAddTarget = 'agent' | 'profile'

interface ProjectScope {
  orgId: string
  projectId: string
}

interface PreparedConfig {
  currentConfigId: string
  render(secretId: string): { source: string; sourceFormat: 'yaml' | 'json' }
}

const SERVER_KEY_PATTERN = /^[a-zA-Z][a-zA-Z0-9-]{0,31}$/

export const zMcpAddBody = zMcpoAuthStartRequest.omit({ owner: true, name: true }).extend({
  server_name: z
    .string()
    .regex(
      SERVER_KEY_PATTERN,
      'must start with a letter and contain at most 32 letters, digits, or hyphens',
    )
    .describe('MCP server key used in the agent config'),
  secret_name: z
    .string()
    .min(1)
    .optional()
    .describe('OAuth secret name; defaults to <server-name>-mcp'),
  browser: zBrowserFlag,
})

type McpAddBody = z.output<typeof zMcpAddBody>

function targetLabel(target: McpAddTarget): string {
  return target === 'agent' ? 'agent' : 'agent profile'
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function serverEntry(url: string, secretId: string): Record<string, unknown> {
  return { url, auth: { type: 'oauth', secret_id: secretId } }
}

function prepareJsonConfig(
  config: AgentConfig,
  serverName: string,
  mcpUrl: string,
): PreparedConfig {
  let parsed: unknown
  try {
    parsed = JSON.parse(config.source ?? '')
  } catch {
    throw new CliInputError('the current agent config contains invalid JSON')
  }
  if (!isRecord(parsed)) throw new CliInputError('the current agent config must be an object')
  const existingMcp = parsed.mcp
  if (existingMcp !== undefined && !isRecord(existingMcp)) {
    throw new CliInputError('the current agent config has an invalid mcp section')
  }
  if (existingMcp !== undefined && Object.hasOwn(existingMcp, serverName)) {
    throw new CliInputError(`MCP server ${serverName} already exists in the current config`)
  }
  return {
    currentConfigId: config.id,
    render(secretId) {
      const mcp = existingMcp ?? {}
      mcp[serverName] = serverEntry(mcpUrl, secretId)
      parsed.mcp = mcp
      return { source: `${JSON.stringify(parsed, null, 2)}\n`, sourceFormat: 'json' }
    },
  }
}

function prepareYamlConfig(
  config: AgentConfig,
  serverName: string,
  mcpUrl: string,
): PreparedConfig {
  const source = config.source ?? ''
  const lines = source.split(/\r?\n/)
  const mcpIndex = lines.findIndex((line) => /^mcp\s*:/.test(line))
  const quotedServerName = serverName.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const entry = (indent: number, secretId: string): string[] => {
    const outer = ' '.repeat(indent)
    const inner = ' '.repeat(indent + 2)
    const auth = ' '.repeat(indent + 4)
    return [
      `${outer}${serverName}:`,
      `${inner}url: ${JSON.stringify(mcpUrl)}`,
      `${inner}auth:`,
      `${auth}type: oauth`,
      `${auth}secret_id: ${JSON.stringify(secretId)}`,
    ]
  }

  let sectionEnd = lines.length
  let childIndent = 2
  if (mcpIndex !== -1) {
    const declaration = lines[mcpIndex] ?? ''
    const remainder = declaration.replace(/^mcp\s*:\s*/, '')
    if (!/^(?:#.*)?$/.test(remainder) && !/^\{\s*\}(?:\s*#.*)?$/.test(remainder)) {
      throw new CliInputError('mcp-add cannot edit an inline YAML mcp mapping')
    }
    for (let index = mcpIndex + 1; index < lines.length; index += 1) {
      const line = lines[index] ?? ''
      if (/^[^\s#]/.test(line)) {
        sectionEnd = index
        break
      }
    }
    const firstEntry = lines
      .slice(mcpIndex + 1, sectionEnd)
      .find((line) => /^\s+\S/.test(line) && !/^\s*#/.test(line))
    const indent = firstEntry?.match(/^(\s+)/)?.[1]?.length
    if (indent !== undefined) childIndent = indent
    const existingPattern = new RegExp(`^\\s{${childIndent}}["']?${quotedServerName}["']?\\s*:`)
    if (lines.slice(mcpIndex + 1, sectionEnd).some((line) => existingPattern.test(line))) {
      throw new CliInputError(`MCP server ${serverName} already exists in the current config`)
    }
  }

  return {
    currentConfigId: config.id,
    render(secretId) {
      if (mcpIndex === -1) {
        const base = source.trimEnd()
        const separator = base === '' ? '' : '\n\n'
        return {
          source: `${base}${separator}mcp:\n${entry(2, secretId).join('\n')}\n`,
          sourceFormat: 'yaml',
        }
      }
      const updated = [...lines]
      if (/^\{\s*\}/.test((updated[mcpIndex] ?? '').replace(/^mcp\s*:\s*/, ''))) {
        updated[mcpIndex] = 'mcp:'
      }
      updated.splice(sectionEnd, 0, ...entry(childIndent, secretId))
      return { source: `${updated.join('\n').trimEnd()}\n`, sourceFormat: 'yaml' }
    },
  }
}

function prepareConfig(config: AgentConfig, serverName: string, mcpUrl: string): PreparedConfig {
  if (config.source === undefined || config.source_format === undefined) {
    throw new CliInputError('the current agent config source is unavailable')
  }
  return config.source_format === 'json'
    ? prepareJsonConfig(config, serverName, mcpUrl)
    : prepareYamlConfig(config, serverName, mcpUrl)
}

async function loadConfig(
  client: OmnaraClient,
  scope: ProjectScope,
  target: McpAddTarget,
  targetId: string,
): Promise<AgentConfig> {
  const path = { orgID: scope.orgId, projectID: scope.projectId }
  if (target === 'profile') {
    const { data } = await sdk.getAgentProfile({
      client,
      path: { ...path, agentProfileID: targetId },
    })
    return data.current_config
  }
  const { data } = await sdk.getAgent({ client, path: { ...path, agentID: targetId } })
  if (data.agent.state !== 'active') throw new CliInputError('cannot update an archived agent')
  const configId = data.agent.current_config_id
  if (configId === undefined) throw new CliInputError('the agent has no current config')
  return (await sdk.getAgentConfig({ client, path: { ...path, agentConfigID: configId } })).data
}

function oauthRequest(body: McpAddBody, projectId: string): McpoAuthStartRequest {
  return {
    owner: { kind: 'project', project_id: projectId },
    name: body.secret_name ?? `${body.server_name}-mcp`,
    mcp_url: body.mcp_url,
    ...(body.return_to !== undefined ? { return_to: body.return_to } : {}),
    ...(body.client_id !== undefined ? { client_id: body.client_id } : {}),
    ...(body.client_secret !== undefined ? { client_secret: body.client_secret } : {}),
    ...(body.scopes !== undefined ? { scopes: body.scopes } : {}),
    ...(body.metadata !== undefined ? { metadata: body.metadata } : {}),
  }
}

async function applyConfig(
  client: OmnaraClient,
  scope: ProjectScope,
  target: McpAddTarget,
  targetId: string,
  prepared: PreparedConfig,
  secret: Secret,
  report: FlowReporter,
): Promise<string> {
  const rendered = prepared.render(secret.id)
  const path = { orgID: scope.orgId, projectID: scope.projectId }
  if (target === 'agent') {
    const { data } = await sdk.updateAgentConfig({
      client,
      path: { ...path, agentID: targetId },
      body: {
        source: rendered.source,
        source_format: rendered.sourceFormat,
        expected_current_config_id: prepared.currentConfigId,
      },
    })
    return data.agent_config.id
  }
  const { data: config } = await sdk.createAgentConfig({
    client,
    path,
    body: { source: rendered.source, source_format: rendered.sourceFormat },
  })
  try {
    await sdk.updateAgentProfile({
      client,
      path: { ...path, agentProfileID: targetId },
      body: { config: config.id, expected_current_config_id: prepared.currentConfigId },
    })
  } catch (error) {
    report.warn(`agent config ${config.id} was created, but the agent profile was not updated`)
    throw error
  }
  return config.id
}

interface McpAddScope {
  orgID: string
  projectID: string
}

async function runMcpAdd(
  context: FlowContext<McpAddScope, McpAddBody>,
  target: McpAddTarget,
  targetId: string,
): Promise<void> {
  const { client, body, report } = context
  const scope: ProjectScope = {
    orgId: context.path.orgID,
    projectId: context.path.projectID,
  }
  const request = oauthRequest(body, scope.projectId)
  const currentConfig = await loadConfig(client, scope, target, targetId)
  const prepared = prepareConfig(currentConfig, body.server_name, request.mcp_url)
  let secret: Secret
  try {
    secret = await authorizeMcpOAuthSecret({
      client,
      orgId: scope.orgId,
      request,
      onAuthorization(url) {
        openAuthorizationUrl(report, 'Authorize this MCP server in your browser', url, body.browser)
        report.start('Waiting for the MCP OAuth secret to be created')
      },
    })
  } catch (error) {
    report.fail('MCP authorization failed')
    throw error
  }
  report.stop('MCP OAuth secret created')
  report.start(`Updating the ${targetLabel(target)} config`)
  let configId: string
  try {
    configId = await applyConfig(client, scope, target, targetId, prepared, secret, report)
  } catch (error) {
    report.fail('Config update failed')
    report.warn(`OAuth secret ${secret.id} was created, but the config was not updated`)
    throw error
  }
  report.stop('Config updated')
  report.info(`Secret ID: ${secret.id}`)
  report.info(`Config ID: ${configId}`)
  report.done()
}

export function runAgentMcpAdd(
  context: FlowContext<McpAddScope & { agentID: string }, McpAddBody>,
): Promise<void> {
  return runMcpAdd(context, 'agent', context.path.agentID)
}

export function runProfileMcpAdd(
  context: FlowContext<McpAddScope & { agentProfileID: string }, McpAddBody>,
): Promise<void> {
  return runMcpAdd(context, 'profile', context.path.agentProfileID)
}
