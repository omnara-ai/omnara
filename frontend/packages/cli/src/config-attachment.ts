import { readFileSync } from 'node:fs'

import {
  type AgentConfigDefinition,
  type CreateAgentConfigRequest,
  type CreateAgentRequest,
  type OmnaraClient,
  sdk,
  zJsonText,
} from '@omnara/sdk'
import { zAgentConfigId } from '@omnara/sdk/zod'
import * as z from 'zod'

import { CliInputError } from './output.ts'

export const zConfigSourceAttachment = z.object({
  file: z
    .string()
    .min(1)
    .optional()
    .describe('path to an agent config file (.yaml, .yml, or .json)'),
  source: z.string().min(1).optional().describe('inline agent config source (YAML or JSON)'),
})

export type ConfigSourceAttachment = z.output<typeof zConfigSourceAttachment>

export const zConfigAttachment = z.object({
  config: zAgentConfigId.optional().describe('existing agent config ID'),
  ...zConfigSourceAttachment.shape,
})

export type ConfigAttachment = z.output<typeof zConfigAttachment>

const ATTACHMENT_HINT = 'pass exactly one of --config, --file, or --source'
const SOURCE_HINT = 'pass exactly one of --file or --source'

function requireExactlyOne(values: (string | undefined)[], hint: string): void {
  const provided = values.filter((value) => value !== undefined)
  if (provided.length !== 1) throw new CliInputError(hint)
}

function fileFormat(filePath: string): 'yaml' | 'json' {
  const lower = filePath.toLowerCase()
  if (lower.endsWith('.json')) return 'json'
  if (lower.endsWith('.yaml') || lower.endsWith('.yml')) return 'yaml'
  throw new CliInputError(`config file must end in .yaml, .yml, or .json: ${filePath}`)
}

// The server validates definitions fully when compiling them, so only the object shape is checked here.
const zDefinitionText = zJsonText.pipe(
  z.custom<AgentConfigDefinition>(
    (value) => z.record(z.string(), z.unknown()).safeParse(value).success,
    'expected a JSON object',
  ),
)

// The server canonicalizes JSON sources, so text and object forms save the same config.
function jsonSource(text: string, label: string): CreateAgentConfigRequest {
  const parsed = zDefinitionText.safeParse(text)
  if (!parsed.success) {
    throw new CliInputError(
      `${label} is not a valid JSON agent config: ${parsed.error.issues[0]?.message}`,
    )
  }
  return { source: text, source_format: 'json' }
}

function inlineSource(source: string): CreateAgentConfigRequest {
  if (!zJsonText.safeParse(source).success) return { source, source_format: 'yaml' }
  return jsonSource(source, 'inline config source')
}

export function renderConfigSource(attachment: ConfigSourceAttachment): CreateAgentConfigRequest {
  requireExactlyOne([attachment.file, attachment.source], SOURCE_HINT)
  if (attachment.file !== undefined) {
    const format = fileFormat(attachment.file)
    let source: string
    try {
      source = readFileSync(attachment.file, 'utf8')
    } catch {
      throw new CliInputError(`could not read config file: ${attachment.file}`)
    }
    if (format === 'yaml') return { source, source_format: 'yaml' }
    return jsonSource(source, `config file ${attachment.file}`)
  }
  if (attachment.source !== undefined) return inlineSource(attachment.source)
  throw new CliInputError(SOURCE_HINT)
}

export interface ProjectScope {
  orgID: string
  projectID: string
}

export interface ProfileScope extends ProjectScope {
  agentProfileID: string
}

export async function resolveConfigId(
  client: OmnaraClient,
  path: ProjectScope,
  attachment: ConfigAttachment,
): Promise<string> {
  requireExactlyOne([attachment.config, attachment.file, attachment.source], ATTACHMENT_HINT)
  if (attachment.config !== undefined) return attachment.config
  const { data } = await sdk.createAgentConfig({
    client,
    path,
    body: renderConfigSource(attachment),
  })
  return data.id
}

const LAUNCH_HINT = 'pass --profile or one of --config, --file, or --source'
const LAUNCH_CONFIG_HINT = 'pass at most one of --config, --file, or --source'

export function renderLaunchConfig(
  profile: string | undefined,
  attachment: ConfigAttachment,
): Pick<CreateAgentRequest, 'config' | 'config_source'> {
  const provided = [attachment.config, attachment.file, attachment.source].filter(
    (value) => value !== undefined,
  )
  if (provided.length > 1) throw new CliInputError(LAUNCH_CONFIG_HINT)
  if (attachment.config !== undefined) return { config: attachment.config }
  if (provided.length === 1) return { config_source: renderConfigSource(attachment) }
  if (profile === undefined) throw new CliInputError(LAUNCH_HINT)
  return {}
}

export async function currentProfileConfigId(
  client: OmnaraClient,
  path: ProfileScope,
): Promise<string> {
  const { data } = await sdk.getAgentProfile({ client, path })
  return data.current_config_id
}
