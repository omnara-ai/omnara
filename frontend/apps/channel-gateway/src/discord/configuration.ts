import type {
  ChannelConnectorAppConfiguration,
  ChannelConnectorInstallationConfiguration,
} from '@omnara/sdk'
import { z } from 'zod'

import { DiscordAPIError, discordID } from './protocol'

const credential = z.object({
  bot_token: z
    .string()
    .min(1)
    .max(4096)
    .regex(/^[\x21-\x7e]+$/),
})

export interface DiscordConfiguration {
  applicationID: string
  botUserID: string
  guildID: string
  botToken: string
}

export function discordAppCredentials(app: ChannelConnectorAppConfiguration) {
  const secret = credential.safeParse(app.credential?.payload)
  const application = discordID.safeParse(app.app.provider_app_ref)
  if (
    app.app.provider !== 'discord' ||
    app.app.connector_key !== 'omnara' ||
    app.credential?.kind !== 'integration_credentials' ||
    !secret.success ||
    !application.success
  )
    throw new DiscordAPIError('invalid_configuration')
  return { applicationID: application.data, botToken: secret.data.bot_token }
}

/** Org app integration_credentials payload is {bot_token:string}. The project
 * install pins a guild and bot account; it does not duplicate the app secret.
 * REST identity verification binds that token to the application and bot user.
 */
export function discordConfiguration(
  app: ChannelConnectorAppConfiguration,
  installation: ChannelConnectorInstallationConfiguration,
): DiscordConfiguration {
  const identity = discordAppCredentials(app)
  const guild = discordID.safeParse(installation.install.provider_tenant_id)
  const bot = discordID.safeParse(installation.install.provider_account_ref)
  if (
    installation.integration_app_id !== app.app.id ||
    installation.app_configuration_revision !== app.app.configuration_revision ||
    !guild.success ||
    !bot.success
  )
    throw new DiscordAPIError('invalid_configuration')
  return {
    applicationID: identity.applicationID,
    botUserID: bot.data,
    guildID: guild.data,
    botToken: identity.botToken,
  }
}
