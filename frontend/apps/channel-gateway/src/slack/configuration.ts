import type { ChannelConnectorInstallationConfiguration } from '@omnara/sdk'
import { z } from 'zod'

import { SlackAPIError } from './client'

export interface SlackCredentials {
  botToken: string
  botUserId: string
}

const nonempty = z.string().refine((value) => value.trim().length > 0)
const credential = z.object({ access_token: nonempty })
const identity = z.object({ bot_user_id: nonempty })

/** Core projects only the installed bot token from the stored combined secret.
 * OAuth client fields and webhook verification secrets never enter this runtime.
 */
export function slackCredentials(
  configuration: ChannelConnectorInstallationConfiguration,
): SlackCredentials {
  if (configuration.credential?.kind !== 'slack_app_credentials') {
    throw new SlackAPIError('invalid_configuration')
  }
  const parsedCredential = credential.safeParse(configuration.credential.payload)
  const parsedIdentity = identity.safeParse(configuration.install.provider_identity)
  if (!parsedCredential.success || !parsedIdentity.success) {
    // Zod errors would include paths and potentially credential values.
    throw new SlackAPIError('invalid_configuration')
  }
  return {
    botToken: parsedCredential.data.access_token,
    botUserId: parsedIdentity.data.bot_user_id,
  }
}
