import type { PersonalAccessToken } from '@omnara/sdk'
import { parse } from 'yaml'

import { type BasicConfig, createBasicConfigSession } from '@/components/agents/useAgentBuilderForm'
import type { CodeContent } from '@/components/overview/CodeBlock'

export const cliLoginCommand = 'npx omnara login'
export const cliSetupPrompt =
  'Read https://omnara.com/SKILL.md and help me create an Omnara agent profile.'
export const cliTokenNamePrefix = 'Omnara CLI'

export function isCliLoginToken(token: PersonalAccessToken) {
  return token.revoked_at == null && token.name.startsWith(cliTokenNamePrefix)
}

export function cliTokenHost(token: PersonalAccessToken) {
  const rest = token.name
    .slice(cliTokenNamePrefix.length)
    .replace(/^\s+on\s+/, '')
    .trim()
  return rest === '' ? token.name : rest
}

function shellLines(parts: string[]) {
  return parts.join(' \\\n  ')
}

export interface ProfileCreateSpec {
  name: string
  json: string
  command: CodeContent
}

export function profileCreateSpec(input: {
  orgId: string
  projectId: string
  name: string
  config: BasicConfig
}): ProfileCreateSpec {
  const source: unknown = parse(createBasicConfigSession('').apply(input.config))
  const json = JSON.stringify(source ?? {}, null, 1).replaceAll(/\n\s*/g, ' ')
  const shellJson = json.replaceAll("'", "'\\''")
  const prefix = shellLines([
    'npx omnara profiles create',
    `--org ${input.orgId}`,
    `--project ${input.projectId}`,
    `--name "${input.name}"`,
    "--source '",
  ])
  return {
    name: input.name,
    json,
    command: {
      copy: `${prefix}${shellJson}'`,
      segments: [prefix, { json }, "'"],
      language: 'shell',
    },
  }
}

export const chatMessage = 'Hi! What can you do?'

export function chatCommands(input: {
  origin: string
  orgId: string
  projectId: string
  profileId: string
  configId: string
}): { cli: CodeContent; sdk: CodeContent; curl: CodeContent } {
  const message = chatMessage
  const cli = shellLines([
    'npx omnara agents launch',
    `--org ${input.orgId}`,
    `--project ${input.projectId}`,
    `--profile ${input.profileId}`,
    `--config ${input.configId}`,
    `--message "${message}"`,
  ])
  const sdk = `import { bearerToken, createOmnaraClient, sdk } from '@omnara/sdk'

const client = createOmnaraClient({
  baseUrl: '${input.origin}',
  auth: bearerToken(process.env.OMNARA_TOKEN),
})

const launched = await sdk.createAgent({
  client,
  path: { orgID: '${input.orgId}', projectID: '${input.projectId}' },
  body: { profile: '${input.profileId}', config: '${input.configId}', message: '${message}' },
})
console.log(launched.data.agent.id)`
  const curl = `curl "${input.origin}/api/v1/orgs/${input.orgId}/projects/${input.projectId}/agents" \\
  -H "Authorization: Bearer $OMNARA_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{ "profile": "${input.profileId}", "config": "${input.configId}", "message": "${message}" }'`
  return {
    cli: { copy: cli, segments: [cli], language: 'shell' },
    sdk: { copy: sdk, segments: [sdk], language: 'typescript' },
    curl: { copy: curl, segments: [curl], language: 'shell' },
  }
}
