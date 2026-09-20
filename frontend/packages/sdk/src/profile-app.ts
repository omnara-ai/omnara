import * as z from 'zod'

import type {
  AppLauncher,
  AppLaunchSlot,
  AppProviderConfig,
  SaveProjectAppRequest,
} from './generated/types.gen'
import { zAgentProfileId, zProjectAppName } from './generated/zod.gen'

export type ProfileAppProvider = 'slack' | 'github' | 'discord'

const discordPublicKeyPattern = '[a-fA-F0-9]{64}'
const discordPublicKey = z.string().regex(new RegExp(`^${discordPublicKeyPattern}$`))

/** Discord launchers include an interaction handler, even with one profile. */
export function profileAppDiscordKeyStatus(input: {
  definition?: string
  slots: readonly Pick<AppLaunchSlot, 'agent_profile_id' | 'agent_id'>[]
  interactions: boolean
  providerConfig?: AppProviderConfig
}) {
  const required =
    input.definition === 'omnara.discord' && (input.slots.length > 0 || input.interactions)
  return {
    required,
    missing: required && !discordPublicKey.safeParse(input.providerConfig?.public_key).success,
    pattern: discordPublicKeyPattern,
  }
}

/** Guided launcher defaults; the server validates identity and authority. */
export function profileAppSetup(input: {
  provider: ProfileAppProvider
  name: string
  profileId?: string
  /** Full offered list for Slack/Discord; when supplied, replaces profileId. */
  profileIds?: readonly string[]
  /** Defaults to true; use false to create a metadata-only draft. */
  launcher?: boolean
  scopeRef?: string
  scopeKind?: 'workspace' | 'channel' | 'repository'
  trigger?: 'mention' | 'pull_request_opened'
}): SaveProjectAppRequest {
  const { provider } = input
  const name = zProjectAppName.parse(input.name)
  const setup: SaveProjectAppRequest = {
    name,
    definition_id: `omnara.${provider}`,
    settings: {},
  }
  if (input.launcher !== false) {
    const profileIds = parseProfileIds(
      input.profileIds ?? (input.profileId ? [input.profileId] : []),
    )
    if (profileIds.length === 0) throw new Error('Choose at least one profile.')
    if (provider === 'github' && profileIds.length !== 1) {
      throw new Error('The GitHub setup helper requires one profile.')
    }
    setup.settings.launcher = {
      ...profileAppLauncherScope(input),
      slots: profileIds.map((id, index) => ({
        key: index === 0 ? 'default' : `profile_${index + 1}`,
        agent_profile_id: id,
      })),
    }
  }
  return setup
}

/**
 * Validate scopes offered by guided setup. The catalog describes capability configs,
 * not launcher scopes; the server remains authoritative for identity and routing.
 * Saved advanced scopes should be preserved unless the user edits them.
 */
export function profileAppLauncherScope(input: {
  provider: ProfileAppProvider
  scopeKind?: string
  scopeRef?: string
  trigger?: string
}): Pick<AppLauncher, 'scope_kind' | 'scope_ref' | 'trigger'> {
  const { provider } = input
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
  if (
    !(provider === 'github' ? ['mention', 'pull_request_opened'] : ['mention']).includes(trigger)
  ) {
    throw new Error('The launch trigger does not belong to this provider.')
  }
  let scopeRef = input.scopeRef?.trim() ?? ''
  if (provider === 'slack') {
    if (
      scopeKind === 'workspace' ? !/^T[A-Z0-9]+$/.test(scopeRef) : !/^[CG][A-Z0-9]+$/.test(scopeRef)
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
  return { trigger, scope_kind: scopeKind, scope_ref: scopeRef }
}

/** Edit only offered chat profiles. Existing-agent slots and all other settings survive unchanged. */
export function profileAppProfileUpdate(
  app: SaveProjectAppRequest,
  profileIds: readonly string[],
): SaveProjectAppRequest {
  const { launcher } = app.settings
  if (!launcher || !['omnara.slack', 'omnara.discord'].includes(app.definition_id)) {
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
    definition_id: app.definition_id,
    settings: { ...app.settings, launcher: { ...launcher, slots } },
  }
}

function parseProfileIds(ids: readonly string[]) {
  if (ids.length > 16) throw new Error('Choose at most 16 profiles.')
  const parsed = ids.map((id) => zAgentProfileId.parse(id))
  if (new Set(parsed).size !== parsed.length) throw new Error('Choose each profile only once.')
  return parsed
}
