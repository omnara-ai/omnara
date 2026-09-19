import {
  profileAppDiscordKeyStatus,
  profileAppProfileUpdate,
  profileAppSetup,
  profileAppTools,
  sdk,
} from '@omnara/sdk'
import * as schemas from '@omnara/sdk/zod'
import * as z from 'zod'

import { type CommandGroup, type FlowContext, flowOp, op } from './factory.ts'
import { formatRecord, formatTable, formatVoid } from './format.ts'
import { CliInputError } from './output.ts'

const zProfileAppBody = z.object({
  name: schemas.zResourceName.describe('project app setup name'),
  connection: schemas.zIntegrationConnectionId.describe('active project connection ID'),
  tools: z
    .array(z.string())
    .optional()
    .describe('selected provider tool name; defaults to all provider tools'),
  listen: z.boolean().default(true).describe('receive later conversation messages'),
})
export const zGitHubAppBody = zProfileAppBody.extend({
  repository_id: z
    .string()
    .regex(/^[1-9][0-9]*$/)
    .describe('numeric GitHub repository ID, not owner/repository'),
})
export const zDiscordAppBody = zProfileAppBody.extend({
  profile_ids: z
    .array(schemas.zAgentProfileId)
    .min(1)
    .max(16)
    .optional()
    .describe(
      'repeat for each offered profile; defaults to the positional profile; one launches immediately, many show a chooser',
    ),
  channel_id: z
    .string()
    .regex(/^[1-9][0-9]*$/)
    .describe('Discord channel ID'),
  interactions: z
    .boolean()
    .default(false)
    .describe('present agent questions and approvals in Discord'),
})
interface ProfilePath {
  orgID: string
  projectID: string
  agentProfileID: string
}

export async function runGitHubAppSetup(
  context: FlowContext<ProfilePath, z.output<typeof zGitHubAppBody>>,
) {
  await createProfileApp(context, 'github', context.body.repository_id, false)
}
export async function runDiscordAppSetup(
  context: FlowContext<ProfilePath, z.output<typeof zDiscordAppBody>>,
) {
  await createProfileApp(
    context,
    'discord',
    context.body.channel_id,
    context.body.interactions,
    context.body.profile_ids,
  )
}

async function createProfileApp(
  context: FlowContext<ProfilePath, z.output<typeof zProfileAppBody>>,
  provider: 'github' | 'discord',
  scopeRef: string,
  interactions: boolean,
  profileIds?: readonly string[],
) {
  const { client, path, body, report } = context
  const project = { orgID: path.orgID, projectID: path.projectID }
  const { data: connection } = await sdk.getIntegrationConnection({
    client,
    path: { ...project, integrationConnectionID: body.connection },
  })
  if (connection.provider !== provider || connection.state !== 'active') {
    throw new CliInputError(`Choose an active ${provider} connection in this project.`)
  }
  const setup = profileAppSetup({
    provider,
    name: body.name,
    connectionId: connection.id,
    profileId: path.agentProfileID,
    profileIds,
    scopeRef,
    tools: body.tools ?? profileAppTools[provider],
    listen: body.listen,
    interactions,
  })
  const discordKey = profileAppDiscordKeyStatus({
    definition: setup.settings.resource.definition,
    slots: setup.settings.launcher?.slots ?? [],
    interactions,
    providerConfig: connection.provider_config,
  })
  if (discordKey.required) {
    if (discordKey.missing) {
      throw new CliInputError(
        'Save a valid public_key on the Discord connection before enabling multiple choices or agent questions.',
      )
    }
    report.info(
      `Discord Interactions Endpoint URL: ${new URL(context.apiUrl).origin}/api/integrations/discord/${connection.id}/interactions`,
    )
  }
  const { data: app } = await sdk.createProjectApp({ client, path: project, body: setup })
  report.info(`App ID: ${app.id}`)
  report.info(`Connection ID: ${connection.id}`)
  if (provider === 'github') {
    const origin = new URL(context.apiUrl).origin
    report.info(
      `GitHub App webhook URL: ${origin}/api/integrations/github/${connection.provider_tenant_id}/events`,
    )
    report.info('Subscribe to pull requests, issue comments, and pull request review comments.')
  } else {
    report.info(
      'One eligible profile launches immediately; multiple eligible profiles show a native menu to choose just one.',
    )
    report.info(
      `Edit the offered list later with apps profiles ${app.id} --profile-ids <profile ID> (repeat --profile-ids for each profile).`,
    )
  }
  report.info('Setup saved. Provider access and the profile’s tool permissions still apply.')
  report.done()
}

export const zAppProfilesBody = z.object({
  profile_ids: z
    .array(schemas.zAgentProfileId)
    .max(16)
    .describe(
      'repeat for each offered profile; replaces the profile list and keeps existing-agent slots and other settings',
    ),
})

export async function runAppProfilesUpdate(
  context: FlowContext<
    z.output<typeof schemas.zGetProjectAppPath>,
    z.output<typeof zAppProfilesBody>
  >,
) {
  const { client, path, body, report } = context
  const { data: app } = await sdk.getProjectApp({ client, path })
  const update = profileAppProfileUpdate(app, body.profile_ids)
  const discordKeyInput = {
    definition: update.settings.resource.definition,
    slots: update.settings.launcher?.slots ?? [],
    interactions: Boolean(update.settings.resource.interaction_handler),
  }
  if (profileAppDiscordKeyStatus(discordKeyInput).required) {
    const connectionId = update.settings.resource.connection
    if (!connectionId) throw new CliInputError('This Discord setup has no saved connection.')
    const { data: connection } = await sdk.getIntegrationConnection({
      client,
      path: { orgID: path.orgID, projectID: path.projectID, integrationConnectionID: connectionId },
    })
    if (
      profileAppDiscordKeyStatus({ ...discordKeyInput, providerConfig: connection.provider_config })
        .missing
    ) {
      throw new CliInputError(
        'Save a valid public_key on the Discord connection before enabling multiple choices or agent questions.',
      )
    }
    report.info(
      `Discord Interactions Endpoint URL: ${new URL(context.apiUrl).origin}/api/integrations/discord/${connection.id}/interactions`,
    )
  }
  await sdk.updateProjectApp({ client, path, body: update })
  report.info(
    'Offered profiles saved. One eligible profile launches immediately; multiple choices show a native menu to select just one.',
  )
  report.done()
}

export const appCommandGroups: CommandGroup[] = [
  {
    name: 'connections',
    summary: 'Manage project provider connections and credentials',
    operations: [
      op({
        verb: 'list',
        summary: 'List project connections',
        fn: sdk.listIntegrationConnections,
        path: schemas.zListIntegrationConnectionsPath,
        query: schemas.zListIntegrationConnectionsQuery,
        format: formatTable([
          'id',
          'provider',
          'provider_tenant_id',
          'provider_account_ref',
          'state',
        ]),
      }),
      op({
        verb: 'get',
        summary: 'Read a connection',
        fn: sdk.getIntegrationConnection,
        path: schemas.zGetIntegrationConnectionPath,
        format: formatRecord(),
      }),
      op({
        verb: 'create',
        summary: 'Create a GitHub or Discord connection; use profiles slack for Slack OAuth',
        fn: sdk.createIntegrationConnection,
        path: schemas.zCreateIntegrationConnectionPath,
        body: schemas.zCreateIntegrationConnectionBody,
        format: formatRecord(),
      }),
      op({
        verb: 'update',
        summary:
          'Replace mutable connection settings; include credentials, config, and state to preserve them',
        fn: sdk.updateIntegrationConnection,
        path: schemas.zUpdateIntegrationConnectionPath,
        body: schemas.zUpdateIntegrationConnectionBody,
        format: formatRecord(),
      }),
      op({
        verb: 'delete',
        summary: 'Disconnect provider access for every app and agent using this connection',
        fn: sdk.deleteIntegrationConnection,
        path: schemas.zDeleteIntegrationConnectionPath,
        format: formatVoid('disconnected'),
      }),
    ],
  },
  {
    name: 'apps',
    summary: 'Manage reusable project apps and launchers',
    operations: [
      flowOp({
        verb: 'profiles',
        summary:
          'Edit offered Slack or Discord profiles; preserve existing-agent slots and saved settings',
        path: schemas.zGetProjectAppPath,
        body: zAppProfilesBody,
        run: runAppProfilesUpdate,
      }),
      op({
        verb: 'list',
        summary: 'List project apps',
        fn: sdk.listProjectApps,
        path: schemas.zListProjectAppsPath,
        query: schemas.zListProjectAppsQuery,
        format: formatTable(['id', 'name', 'enabled']),
      }),
      op({
        verb: 'get',
        summary: 'Read app settings and launcher',
        fn: sdk.getProjectApp,
        path: schemas.zGetProjectAppPath,
        format: formatRecord(),
      }),
      op({
        verb: 'create',
        summary: 'Create reusable app setup',
        fn: sdk.createProjectApp,
        path: schemas.zCreateProjectAppPath,
        body: schemas.zCreateProjectAppBody,
        format: formatRecord(),
      }),
      op({
        verb: 'update',
        summary: 'Replace app settings; compiled agent configs keep their existing settings',
        fn: sdk.updateProjectApp,
        path: schemas.zUpdateProjectAppPath,
        body: schemas.zUpdateProjectAppBody,
        format: formatRecord(),
      }),
      op({
        verb: 'delete',
        summary: 'Remove app setup; retain the connection and existing agents',
        fn: sdk.deleteProjectApp,
        path: schemas.zDeleteProjectAppPath,
        format: formatVoid('deleted'),
      }),
    ],
  },
]
