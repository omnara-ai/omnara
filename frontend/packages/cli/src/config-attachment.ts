import { readFileSync } from 'node:fs'

import { type OmnaraClient, sdk } from '@omnara/sdk'
import { zAgentConfigId } from '@omnara/sdk/zod'
import * as z from 'zod'

import { CliInputError } from './output.ts'

const configSourceFields = {
  file: z
    .string()
    .min(1)
    .optional()
    .describe('path to an agent config file (.yaml, .yml, or .json)'),
  source: z.string().min(1).optional().describe('inline agent config source (YAML or JSON)'),
}

export const zConfigSourceAttachment = z.object(configSourceFields)

export type ConfigSourceAttachment = z.output<typeof zConfigSourceAttachment>

export const zConfigAttachment = z.object({
  config: zAgentConfigId.optional().describe('existing agent config ID'),
  ...configSourceFields,
})

export type ConfigAttachment = z.output<typeof zConfigAttachment>

export interface ConfigSource {
  source: string
  source_format: 'yaml' | 'json'
}

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

function inlineFormat(source: string): 'yaml' | 'json' {
  try {
    JSON.parse(source)
    return 'json'
  } catch {
    return 'yaml'
  }
}

export function renderConfigSource(attachment: ConfigSourceAttachment): ConfigSource {
  requireExactlyOne([attachment.file, attachment.source], SOURCE_HINT)
  if (attachment.file !== undefined) {
    const format = fileFormat(attachment.file)
    let source: string
    try {
      source = readFileSync(attachment.file, 'utf8')
    } catch {
      throw new CliInputError(`could not read config file: ${attachment.file}`)
    }
    return { source, source_format: format }
  }
  if (attachment.source !== undefined) {
    return { source: attachment.source, source_format: inlineFormat(attachment.source) }
  }
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
