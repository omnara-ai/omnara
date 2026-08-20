import { readFileSync } from 'node:fs'

import { type OmnaraClient, sdk } from '@omnara/sdk'
import { zAgentConfigId } from '@omnara/sdk/zod'
import * as z from 'zod'

import { CliInputError } from './output.ts'

export const zConfigAttachment = z.object({
  config: zAgentConfigId.optional().describe('existing agent config ID'),
  file: z
    .string()
    .min(1)
    .optional()
    .describe('path to an agent config file (.yaml, .yml, or .json)'),
  source: z.string().min(1).optional().describe('inline agent config source (YAML or JSON)'),
})

export type ConfigAttachment = z.output<typeof zConfigAttachment>

export interface ConfigSource {
  source: string
  source_format: 'yaml' | 'json'
}

const ATTACHMENT_HINT = 'pass exactly one of --config, --file, or --source'

const zProjectScope = z.object({
  orgID: z.string().min(1),
  projectID: z.string().min(1),
})

function requireExactlyOne(attachment: ConfigAttachment): void {
  const provided = [attachment.config, attachment.file, attachment.source].filter(
    (value) => value !== undefined,
  )
  if (provided.length !== 1) throw new CliInputError(ATTACHMENT_HINT)
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

export function renderConfigAttachment(attachment: ConfigAttachment): ConfigSource {
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
  throw new CliInputError(ATTACHMENT_HINT)
}

export async function resolveConfigId(
  client: OmnaraClient,
  path: Record<string, unknown>,
  attachment: ConfigAttachment,
): Promise<string> {
  requireExactlyOne(attachment)
  if (attachment.config !== undefined) return attachment.config
  const { data } = await sdk.createAgentConfig({
    client,
    path: zProjectScope.parse(path),
    body: renderConfigAttachment(attachment),
  })
  return data.id
}

export async function resolveConfigSource(
  client: OmnaraClient,
  path: Record<string, unknown>,
  attachment: ConfigAttachment,
): Promise<ConfigSource> {
  requireExactlyOne(attachment)
  if (attachment.config === undefined) return renderConfigAttachment(attachment)
  const { data } = await sdk.getAgentConfig({
    client,
    path: { ...zProjectScope.parse(path), agentConfigID: attachment.config },
  })
  if (data.source === undefined || data.source_format === undefined) {
    throw new CliInputError(`the source of agent config ${attachment.config} is unavailable`)
  }
  return { source: data.source, source_format: data.source_format }
}

const zProfilePath = zProjectScope.extend({ agentProfileID: z.string().min(1) })

export async function currentProfileConfigId(
  client: OmnaraClient,
  path: Record<string, unknown>,
): Promise<string> {
  const { data } = await sdk.getAgentProfile({ client, path: zProfilePath.parse(path) })
  return data.current_config_id
}
