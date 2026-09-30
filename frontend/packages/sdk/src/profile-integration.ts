import * as z from 'zod'

import type {
  CreateIntegrationRequest,
  IntegrationKind,
  IntegrationProviderConfig,
  IntegrationSettings,
  UpdateIntegrationRequest,
} from './generated/types.gen'
import { zAgentProfileId, zIntegrationName } from './generated/zod.gen'

const discordPublicKeyPattern = '[a-fA-F0-9]{64}'
const discordPublicKey = z.string().regex(new RegExp(`^${discordPublicKeyPattern}$`))
const profilesSchema = z
  .array(zAgentProfileId)
  .max(16)
  .refine((ids) => new Set(ids).size === ids.length, 'Choose each profile only once.')
const channelSchema = z
  .string()
  .trim()
  .regex(/^[CG][A-Z0-9]+$/, 'Enter a Slack channel ID, such as C123.')
const repositorySchema = z
  .string()
  .trim()
  .regex(/^[1-9][0-9]*$/, 'Enter a positive repository ID without leading zeros.')
  .refine((id) => BigInt(id) <= 9223372036854775807n, 'The repository ID is too large.')
const triggerSchema = z.enum(['mention', 'pull_request_opened', 'both'])
const chatLauncherSchema = z
  .object({ profiles: profilesSchema.min(1), channel_id: channelSchema.optional() })
  .strict()
const githubLauncherSchema = z
  .object({
    profile: zAgentProfileId,
    trigger: triggerSchema,
    repository_id: repositorySchema.optional(),
  })
  .strict()

export type ChatIntegrationLauncher = z.output<typeof chatLauncherSchema>
export type GitHubIntegrationLauncher = z.output<typeof githubLauncherSchema>

export function chatIntegrationLauncher(settings: IntegrationSettings) {
  return settings.launcher === undefined ? undefined : chatLauncherSchema.parse(settings.launcher)
}

export function githubIntegrationSettings(settings: IntegrationSettings) {
  return {
    launcher:
      settings.launcher === undefined ? undefined : githubLauncherSchema.parse(settings.launcher),
  }
}

export function profileIntegrationProfiles(integration: {
  integration_kind: IntegrationKind
  settings: IntegrationSettings
}) {
  if (integration.integration_kind === 'github_pr') {
    const launcher = githubIntegrationSettings(integration.settings).launcher
    return launcher ? [launcher.profile] : []
  }
  return chatIntegrationLauncher(integration.settings)?.profiles ?? []
}

export function profileIntegrationDiscordKeyStatus(input: {
  integrationKind?: IntegrationKind
  launcher: boolean
  interactions: boolean
  providerConfig?: IntegrationProviderConfig
}) {
  const required =
    input.integrationKind === 'discord_thread' && (input.launcher || input.interactions)
  return {
    required,
    missing: required && !discordPublicKey.safeParse(input.providerConfig?.public_key).success,
    pattern: discordPublicKeyPattern,
  }
}

export function profileIntegrationSetup(input: {
  integrationKind: IntegrationKind
  name: string
  profileId?: string
  /** Ordered alternatives for Slack/Discord; replaces profileId when supplied. */
  profileIds?: readonly string[]
  /** Defaults to true; use false to create a metadata-only draft. */
  launcher?: boolean
  channelId?: string
  repositoryId?: string
  trigger?: GitHubIntegrationLauncher['trigger']
}): CreateIntegrationRequest {
  const settings: IntegrationSettings = {}
  if (input.launcher !== false) {
    const profiles = profilesSchema
      .min(1, 'Choose at least one profile.')
      .parse(input.profileIds ?? (input.profileId ? [input.profileId] : []))
    if (input.integrationKind === 'github_pr') {
      if (profiles.length !== 1) throw new Error('GitHub requires exactly one profile.')
      if (input.channelId) throw new Error('GitHub launchers do not accept a channel.')
      settings.launcher = githubLauncherSchema.parse({
        profile: profiles[0],
        trigger: input.trigger ?? 'both',
        repository_id: input.repositoryId === '' ? undefined : input.repositoryId,
      })
    } else {
      if (input.repositoryId || (input.trigger && input.trigger !== 'mention'))
        throw new Error('Chat launchers start from mentions.')
      if (input.integrationKind === 'discord_thread' && input.channelId)
        throw new Error('Manage Discord bot access in Discord.')
      settings.launcher = chatLauncherSchema.parse({
        profiles,
        channel_id: input.channelId === '' ? undefined : input.channelId,
      })
    }
  }
  const name = zIntegrationName.safeParse(input.name.trim())
  if (!name.success)
    throw new Error('Use 1–32 letters, numbers or hyphens, starting with a letter.')
  return { name: name.data, integration_kind: input.integrationKind, settings }
}

export function profileIntegrationProfileUpdate(
  integration: Pick<CreateIntegrationRequest, 'integration_kind' | 'settings'>,
  profileIds: readonly string[],
): UpdateIntegrationRequest {
  if (!['slack_thread', 'discord_thread'].includes(integration.integration_kind))
    throw new Error('Profile editing requires a Slack or Discord integration.')
  const profiles = profilesSchema.parse(profileIds)
  const settings = { ...integration.settings }
  if (profiles.length === 0) delete settings.launcher
  else settings.launcher = { ...chatIntegrationLauncher(settings), profiles }
  return { settings }
}
