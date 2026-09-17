import type {
  ChannelConnectorAppConfiguration,
  ChannelConnectorInstallationConfiguration,
} from '@omnara/sdk'
import { z } from 'zod'

import { GitHubAPIError, githubNodeID } from './protocol'

const decimalID = z
  .string()
  .regex(/^[1-9][0-9]{0,15}$/)
  .refine((id) => Number.isSafeInteger(Number(id)))
const credential = z.object({
  private_key: z
    .string()
    .min(1)
    .max(32 * 1024),
  webhook_secret: z.string().min(1).max(4096),
})
const repository = z.object({
  repository_owner: z.string().regex(/^[A-Za-z0-9][A-Za-z0-9-]{0,99}$/),
  repository_name: z.string().regex(/^[A-Za-z0-9_.-]{1,100}$/),
  repository_node_id: githubNodeID,
})

export interface GitHubConfiguration {
  appID: string
  privateKey: string
  webhookSecret: string
  installationID: number
  repositoryID: number
  repositoryNodeID: string
  repositoryOwner: string
  repositoryName: string
  projectID: string
  integrationInstallID: string
}

/** App credentials stay in the gateway. OAuth client fields are not runtime inputs.
 * Configuration stores installation IDs as numbers; reject unsupported IDs rather
 * than authenticating a silently rounded installation or repository.
 */
export function githubConfiguration(
  app: ChannelConnectorAppConfiguration,
  installation: ChannelConnectorInstallationConfiguration,
): GitHubConfiguration {
  const secret = credential.safeParse(app.credential?.payload)
  const identity = repository.safeParse(installation.install.provider_identity)
  const appID = decimalID.safeParse(app.app.provider_app_ref)
  const installationID = decimalID.safeParse(installation.install.provider_tenant_id)
  const repositoryID = decimalID.safeParse(installation.install.provider_account_ref)
  if (
    app.app.provider !== 'github' ||
    installation.integration_app_id !== app.app.id ||
    installation.app_configuration_revision !== app.app.configuration_revision ||
    app.credential?.kind !== 'integration_credentials' ||
    !secret.success ||
    !identity.success ||
    !appID.success ||
    !installationID.success ||
    !repositoryID.success ||
    !installation.install.project_id ||
    !installation.install.id
  )
    throw new GitHubAPIError('invalid_configuration')
  return {
    appID: appID.data,
    privateKey: secret.data.private_key,
    webhookSecret: secret.data.webhook_secret,
    installationID: Number(installationID.data),
    repositoryID: Number(repositoryID.data),
    repositoryNodeID: identity.data.repository_node_id,
    repositoryOwner: identity.data.repository_owner,
    repositoryName: identity.data.repository_name,
    projectID: installation.install.project_id,
    integrationInstallID: installation.install.id,
  }
}
