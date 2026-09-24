import * as z from 'zod'

import type {
  IntegrationLauncher,
  IntegrationLaunchSlot,
  IntegrationProviderConfig,
  IntegrationType,
  SaveProjectIntegrationRequest,
} from './generated/types.gen'
import { zAgentProfileId, zProjectIntegrationName } from './generated/zod.gen'

const discordPublicKeyPattern = '[a-fA-F0-9]{64}'
const discordPublicKey = z.string().regex(new RegExp(`^${discordPublicKeyPattern}$`))

export function profileIntegrationDiscordKeyStatus(input: {
  integrationType?: IntegrationType
  slots: readonly Pick<IntegrationLaunchSlot, 'agent_profile_id' | 'agent_id'>[]
  interactions: boolean
  providerConfig?: IntegrationProviderConfig
}) {
  const required =
    input.integrationType === 'discord_thread' && (input.slots.length > 0 || input.interactions)
  return {
    required,
    missing: required && !discordPublicKey.safeParse(input.providerConfig?.public_key).success,
    pattern: discordPublicKeyPattern,
  }
}

export function profileIntegrationSetup(input: {
  integrationType: IntegrationType
  name: string
  profileId?: string
  /** Full offered list for Slack/Discord; when supplied, replaces profileId. */
  profileIds?: readonly string[]
  /** Defaults to true; use false to create a metadata-only draft. */
  launcher?: boolean
  scopeRef?: string
  scopeKind?: 'workspace' | 'channel' | 'repository' | 'installation'
  trigger?: 'mention' | 'pull_request_opened'
}): SaveProjectIntegrationRequest {
  const { integrationType } = input
  const name = zProjectIntegrationName.parse(input.name)
  const setup: SaveProjectIntegrationRequest = {
    name,
    integration_type: integrationType,
    settings: {},
  }
  if (input.launcher !== false) {
    const profileIds = parseProfileIds(
      input.profileIds ?? (input.profileId ? [input.profileId] : []),
    )
    if (profileIds.length === 0) throw new Error('Choose at least one profile.')
    if (integrationType === 'github_pr' && profileIds.length !== 1) {
      throw new Error('The GitHub setup helper requires one profile.')
    }
    setup.settings.launcher = {
      ...profileIntegrationLauncherScope(input),
      slots: profileIds.map((id, index) => ({
        key: index === 0 ? 'default' : `profile_${index + 1}`,
        agent_profile_id: id,
      })),
    }
  }
  return setup
}

export function profileIntegrationLauncherScope(input: {
  integrationType: IntegrationType
  scopeKind?: string
  scopeRef?: string
  trigger?: string
}): Pick<IntegrationLauncher, 'scope_kind' | 'scope_ref' | 'trigger'> {
  const { integrationType } = input
  const trigger =
    input.trigger ?? (integrationType === 'github_pr' ? 'pull_request_opened' : 'mention')
  if (
    !(integrationType === 'github_pr' ? ['mention', 'pull_request_opened'] : ['mention']).includes(
      trigger,
    )
  ) {
    throw new Error('The launch trigger does not belong to this integration type.')
  }
  if (integrationType === 'discord_thread') {
    if (input.scopeKind || input.scopeRef)
      throw new Error('Discord mentions work wherever the bot has access; omit launcher scope.')
    return { trigger }
  }
  const scopeKind =
    input.scopeKind ?? (integrationType === 'slack_thread' ? 'workspace' : 'installation')
  if (
    !(
      integrationType === 'slack_thread' ? ['workspace', 'channel'] : ['repository', 'installation']
    ).includes(scopeKind)
  ) {
    throw new Error('The launcher scope does not belong to this integration type.')
  }
  let scopeRef = input.scopeRef?.trim() ?? ''
  if (integrationType === 'slack_thread') {
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
    if (BigInt(scopeRef) > 9223372036854775807n) throw new Error('The scope ID is too large.')
    scopeRef = BigInt(scopeRef).toString()
  }
  return { trigger, scope_kind: scopeKind, scope_ref: scopeRef }
}

export function profileIntegrationProfileUpdate(
  integration: SaveProjectIntegrationRequest,
  profileIds: readonly string[],
): SaveProjectIntegrationRequest {
  const { launcher } = integration.settings
  if (!launcher || !['slack_thread', 'discord_thread'].includes(integration.integration_type)) {
    throw new Error('Profile editing requires a Slack or Discord launcher.')
  }
  const ids = parseProfileIds(profileIds)
  const slots = launcher.slots.filter(
    (slot) =>
      Boolean(slot.agent_id) || !slot.agent_profile_id || ids.includes(slot.agent_profile_id),
  )
  const keys = new Set(launcher.slots.map((slot) => slot.key))
  for (const id of ids) {
    if (slots.some((slot) => !slot.agent_id && slot.agent_profile_id === id)) continue
    let suffix = 1
    while (keys.has(`profile_${suffix}`)) suffix++
    const slot: IntegrationLaunchSlot = { key: `profile_${suffix}`, agent_profile_id: id }
    keys.add(slot.key)
    slots.push(slot)
  }
  if (slots.length === 0) throw new Error('Choose at least one profile.')
  if (slots.length > 16)
    throw new Error('An integration setup supports at most 16 slots, including existing agents.')
  return {
    name: integration.name,
    integration_type: integration.integration_type,
    settings: { ...integration.settings, launcher: { ...launcher, slots } },
  }
}

function parseProfileIds(ids: readonly string[]) {
  if (ids.length > 16) throw new Error('Choose at most 16 profiles.')
  const parsed = ids.map((id) => zAgentProfileId.parse(id))
  if (new Set(parsed).size !== parsed.length) throw new Error('Choose each profile only once.')
  return parsed
}
