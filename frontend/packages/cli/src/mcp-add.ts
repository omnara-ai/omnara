import {
  type AgentConfig,
  type AgentConfigDefinition,
  type AgentConfigDefinitionMcpServer,
  type CreateAgentConfigRequest,
  type McpoAuthStartRequest,
  type OmnaraClient,
  sdk,
  type Secret,
  zJsonText,
} from '@omnara/sdk'
import { zMcpoAuthStartRequest, zResourceName } from '@omnara/sdk/zod'
import { isMap, isScalar, parseDocument } from 'yaml'
import * as z from 'zod'

import type { FlowContext } from './factory.ts'
import { authorizeMcpOAuthSecret, openAuthorizationUrl, zBrowserFlag } from './mcp-oauth.ts'
import { CliInputError } from './output.ts'
import type { FlowReporter } from './reporter.ts'

type McpAddTarget = 'agent' | 'profile'

interface ProjectScope {
  orgId: string
  projectId: string
}

interface PreparedConfig {
  currentConfigId: string
  render(secretId: string): CreateAgentConfigRequest
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
  secret_name: zResourceName
    .optional()
    .describe('OAuth secret name; defaults to <server-name>-mcp'),
  browser: zBrowserFlag,
})

type McpAddBody = z.output<typeof zMcpAddBody>

function targetLabel(target: McpAddTarget): string {
  return target === 'agent' ? 'agent' : 'agent profile'
}

function serverEntry(url: string, secretId: string): AgentConfigDefinitionMcpServer {
  return { url, auth: { type: 'oauth', secret_id: secretId } }
}

// The server validates the full definition.
const zJsonRecord = z.record(z.string(), z.json())
const zDefinitionText = zJsonText.pipe(
  z.custom<AgentConfigDefinition>((value) => zJsonRecord.safeParse(value).success),
)

function prepareJsonConfig(
  configId: string,
  source: string,
  serverName: string,
  mcpUrl: string,
): PreparedConfig {
  const parsed = zDefinitionText.safeParse(source)
  if (!parsed.success) throw new CliInputError('the current agent config is not a JSON object')
  const definition = parsed.data
  if (!zJsonRecord.nullish().safeParse(definition.mcp).success) {
    throw new CliInputError('the current agent config has an invalid mcp section')
  }
  if (definition.mcp != null && Object.hasOwn(definition.mcp, serverName)) {
    throw new CliInputError(`MCP server ${serverName} already exists in the current config`)
  }
  return {
    currentConfigId: configId,
    render(secretId) {
      return {
        source: {
          ...definition,
          mcp: { ...definition.mcp, [serverName]: serverEntry(mcpUrl, secretId) },
        },
        source_format: 'json',
      }
    },
  }
}

function prepareYamlConfig(
  configId: string,
  source: string,
  serverName: string,
  mcpUrl: string,
): PreparedConfig {
  const doc = parseDocument(source)
  if (doc.errors.length > 0) {
    throw new CliInputError('the current agent config contains invalid YAML')
  }
  if (doc.contents !== null && !isMap(doc.contents)) {
    throw new CliInputError('the current agent config must be an object')
  }
  const mcp = doc.get('mcp', true)
  const emptyMcp = isScalar(mcp) && mcp.value === null
  if (mcp !== undefined && !emptyMcp && !isMap(mcp)) {
    throw new CliInputError('the current agent config has an invalid mcp section')
  }
  if (isMap(mcp) && doc.hasIn(['mcp', serverName])) {
    throw new CliInputError(`MCP server ${serverName} already exists in the current config`)
  }
  return {
    currentConfigId: configId,
    render(secretId) {
      if (isMap(mcp)) {
        doc.setIn(['mcp', serverName], serverEntry(mcpUrl, secretId))
      } else {
        doc.set('mcp', { [serverName]: serverEntry(mcpUrl, secretId) })
      }
      return { source: doc.toString(), source_format: 'yaml' }
    },
  }
}

function prepareConfig(config: AgentConfig, serverName: string, mcpUrl: string): PreparedConfig {
  if (config.source === undefined || config.source_format === undefined) {
    throw new CliInputError('the current agent config source is unavailable')
  }
  return config.source_format === 'json'
    ? prepareJsonConfig(config.id, config.source, serverName, mcpUrl)
    : prepareYamlConfig(config.id, config.source, serverName, mcpUrl)
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
      body: { ...rendered, expected_current_config_id: prepared.currentConfigId },
    })
    return data.agent_config.id
  }
  const { data: config } = await sdk.createAgentConfig({
    client,
    path,
    body: rendered,
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
  const { server_name, secret_name, browser, ...oauthFields } = body
  const request: McpoAuthStartRequest = {
    ...oauthFields,
    owner: { kind: 'project', project_id: scope.projectId },
    name: secret_name ?? `${server_name}-mcp`,
  }
  const currentConfig = await loadConfig(client, scope, target, targetId)
  const prepared = prepareConfig(currentConfig, server_name, request.mcp_url)
  let secret: Secret
  try {
    secret = await authorizeMcpOAuthSecret({
      client,
      orgId: scope.orgId,
      request,
      onAuthorization(url) {
        openAuthorizationUrl(report, 'Authorize this MCP server in your browser', url, browser)
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
    report.warn(
      `OAuth secret ${secret.id} was created, but the ${targetLabel(target)} was not updated`,
    )
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
