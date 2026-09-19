import * as z from 'zod'

import type {
  AppLaunchSlot,
  IntegrationConnection,
  SaveProjectAppRequest,
} from './generated/types.gen'
import { zAgentProfileId, zIntegrationConnectionId, zResourceName } from './generated/zod.gen'

export type ProfileAppProvider = 'slack' | 'github' | 'discord'

export const profileAppTools: Record<ProfileAppProvider, readonly string[]> = {
  slack: ['slack_read', 'slack_post_message'],
  github: ['github_read', 'github_discussion_comment', 'github_inline_comment', 'github_reply'],
  discord: ['discord_read', 'discord_post_message'],
}

const discordPublicKeyPattern = '[a-fA-F0-9]{64}'
const discordPublicKey = z.string().regex(new RegExp(`^${discordPublicKeyPattern}$`))

/** Chooser options count profile slots, including duplicates; fixed-agent slots need no menu. */
export function profileAppDiscordKeyStatus(input: {
  definition?: string
  slots: readonly Pick<AppLaunchSlot, 'agent_profile_id' | 'agent_id'>[]
  interactions: boolean
  providerConfig?: IntegrationConnection['provider_config']
}) {
  const profileCount = input.slots.filter(
    (slot) => !slot.agent_id && Boolean(slot.agent_profile_id),
  ).length
  const required = input.definition === 'omnara.discord' && (profileCount > 1 || input.interactions)
  return {
    required,
    missing: required && !discordPublicKey.safeParse(input.providerConfig?.public_key).success,
    pattern: discordPublicKeyPattern,
  }
}

/** Explicit setup defaults shared by the console and CLI; the server validates authority. */
export function profileAppSetup(input: {
  provider: ProfileAppProvider
  name: string
  /** May be omitted while validating a new connection; set it before saving a launcher. */
  connectionId?: string
  profileId?: string
  /** Full offered list for Slack/Discord; when supplied, replaces profileId. */
  profileIds?: readonly string[]
  /** Defaults to true for the CLI's profile-first setup. */
  launcher?: boolean
  scopeRef?: string
  scopeKind?: 'workspace' | 'channel' | 'repository'
  trigger?: 'mention' | 'pull_request_opened'
  tools: readonly string[]
  listen: boolean
  interactions: boolean
}): SaveProjectAppRequest {
  const { provider } = input
  const name = zResourceName.parse(input.name)
  const connection =
    input.connectionId === undefined
      ? undefined
      : zIntegrationConnectionId.parse(input.connectionId)
  if (input.tools.some((tool) => !profileAppTools[provider].includes(tool))) {
    throw new Error('A selected tool does not belong to this provider.')
  }
  if (provider === 'github' && input.interactions) {
    throw new Error('GitHub apps do not support interaction handlers.')
  }
  const setup: SaveProjectAppRequest = {
    name,
    enabled: true,
    settings: {
      resource: {
        definition: `omnara.${provider}`,
        tools: Object.fromEntries(input.tools.map((tool) => [tool, {}])),
      },
    },
  }
  if (connection !== undefined) setup.settings.resource.connection = connection
  if (input.launcher !== false) {
    const profileIds = parseProfileIds(
      input.profileIds ?? (input.profileId ? [input.profileId] : []),
    )
    if (profileIds.length === 0) throw new Error('Choose at least one profile.')
    if (provider === 'github' && profileIds.length !== 1) {
      throw new Error('The GitHub setup helper requires one profile.')
    }
    const scopeKind =
      input.scopeKind ??
      (provider === 'slack' ? 'workspace' : provider === 'github' ? 'repository' : 'channel')
    if (
      !(
        provider === 'slack'
          ? ['workspace', 'channel']
          : provider === 'github'
            ? ['repository']
            : ['channel']
      ).includes(scopeKind)
    ) {
      throw new Error('The launcher scope does not belong to this provider.')
    }
    const trigger = input.trigger ?? (provider === 'github' ? 'pull_request_opened' : 'mention')
    if (trigger === 'pull_request_opened' && provider !== 'github') {
      throw new Error('The launch trigger does not belong to this provider.')
    }
    let scopeRef = input.scopeRef?.trim() ?? ''
    if (provider === 'slack') {
      if (
        scopeKind === 'workspace'
          ? !/^T[A-Z0-9]+$/.test(scopeRef)
          : !/^[CG][A-Z0-9]+$/.test(scopeRef)
      ) {
        throw new Error(
          scopeKind === 'workspace'
            ? 'Enter a Slack workspace ID, such as T123.'
            : 'Enter a Slack channel ID, such as C123.',
        )
      }
    } else {
      if (!/^[1-9][0-9]*$/.test(scopeRef))
        throw new Error('Enter a positive ID without leading zeros.')
      const max = provider === 'github' ? 9223372036854775807n : 18446744073709551615n
      if (BigInt(scopeRef) > max) throw new Error('The scope ID is too large.')
      scopeRef = BigInt(scopeRef).toString()
    }
    setup.settings.launcher = {
      trigger,
      scope_kind: scopeKind,
      scope_ref: scopeRef,
      slots: profileIds.map((id, index) => ({
        key: index === 0 ? 'default' : `profile_${index + 1}`,
        agent_profile_id: id,
      })),
    }
  }
  if (input.listen) {
    setup.settings.resource.listener = {
      events:
        provider === 'github' ? ['discussion_comment', 'review_comment', 'commit'] : ['message'],
    }
  }
  if (input.interactions) {
    setup.settings.resource.interaction_handler = { definition: `omnara.${provider}.interactions` }
  }
  return setup
}

/** Edit only offered chat profiles. Existing-agent slots and all other settings survive unchanged. */
export function profileAppProfileUpdate(
  app: SaveProjectAppRequest,
  profileIds: readonly string[],
): SaveProjectAppRequest {
  const { launcher, resource } = app.settings
  if (!launcher || !['omnara.slack', 'omnara.discord'].includes(resource.definition ?? '')) {
    throw new Error('Profile editing requires a Slack or Discord launcher.')
  }
  const ids = parseProfileIds(profileIds)
  // Keep the original order and keys, including repeated profile slots in saved generic setups.
  const slots = launcher.slots.filter(
    (slot) =>
      Boolean(slot.agent_id) || !slot.agent_profile_id || ids.includes(slot.agent_profile_id),
  )
  const keys = new Set(launcher.slots.map((slot) => slot.key))
  for (const id of ids) {
    if (slots.some((slot) => !slot.agent_id && slot.agent_profile_id === id)) continue
    let suffix = 1
    while (keys.has(`profile_${suffix}`)) suffix++
    const slot: AppLaunchSlot = { key: `profile_${suffix}`, agent_profile_id: id }
    keys.add(slot.key)
    slots.push(slot)
  }
  if (slots.length === 0) throw new Error('Choose at least one profile.')
  if (slots.length > 16)
    throw new Error('An app setup supports at most 16 slots, including existing agents.')
  return {
    name: app.name,
    enabled: app.enabled,
    settings: { ...app.settings, launcher: { ...launcher, slots } },
  }
}

function parseProfileIds(ids: readonly string[]) {
  if (ids.length > 16) throw new Error('Choose at most 16 profiles.')
  const parsed = ids.map((id) => zAgentProfileId.parse(id))
  if (new Set(parsed).size !== parsed.length) throw new Error('Choose each profile only once.')
  return parsed
}
