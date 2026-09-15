import type { ChannelConnectorInstallationConfiguration } from '@omnara/sdk'
import { z } from 'zod'

import { SlackAPIError } from './client'
import type { SlackCredentials } from './messages'

const nonempty = z.string().refine((value) => value.trim().length > 0)
const credential = z.object({ access_token: nonempty, signing_secret: nonempty })
const identity = z.object({ bot_user_id: nonempty })

/** Reuse the existing combined installation credential. OAuth client fields stay
 * in that credential; send/read do not need an app-level secret or tenant value.
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
    signingSecret: parsedCredential.data.signing_secret,
    botUserId: parsedIdentity.data.bot_user_id,
  }
}
