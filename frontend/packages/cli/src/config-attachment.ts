import { readFileSync } from 'node:fs'

import {
  type AgentConfigDefinition,
  type AgentConfigSource,
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

function jsonDefinition(text: string, label: string): AgentConfigDefinition {
  const parsed = zDefinitionText.safeParse(text)
  if (!parsed.success) {
    throw new CliInputError(
      `${label} is not a valid JSON agent config: ${parsed.error.issues[0]?.message}`,
    )
  }
  return parsed.data
}

function inlineSource(source: string): AgentConfigSource {
  if (!zJsonText.safeParse(source).success) return { yaml: source }
  return { definition: jsonDefinition(source, 'inline config source') }
}

export function renderConfigSource(attachment: ConfigSourceAttachment): AgentConfigSource {
  requireExactlyOne([attachment.file, attachment.source], SOURCE_HINT)
  if (attachment.file !== undefined) {
    const format = fileFormat(attachment.file)
    let source: string
    try {
      source = readFileSync(attachment.file, 'utf8')
    } catch {
      throw new CliInputError(`could not read config file: ${attachment.file}`)
    }
    if (format === 'yaml') return { yaml: source }
    return { definition: jsonDefinition(source, `config file ${attachment.file}`) }
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

export async function currentProfileConfigId(
  client: OmnaraClient,
  path: ProfileScope,
): Promise<string> {
  const { data } = await sdk.getAgentProfile({ client, path })
  return data.current_config_id
}
