import { profileIntegrationProfileUpdate, sdk } from '@omnara/sdk'
import * as schemas from '@omnara/sdk/zod'
import * as z from 'zod'

import { type CommandGroup, type FlowContext, flowOp, op } from './factory.ts'
import { formatRecord, formatTable, formatVoid } from './format.ts'
import { runSlackIntegration, zSlackBody } from './slack-setup.ts'

export const zIntegrationProfilesBody = z.object({
  profile_ids: z
    .array(schemas.zAgentProfileId)
    .max(16)
    .describe(
      'repeat for each offered profile; keeps existing-agent slots and other integration settings',
    ),
})

export async function runIntegrationProfilesUpdate(
  context: FlowContext<
    z.output<typeof schemas.zGetProjectIntegrationPath>,
    z.output<typeof zIntegrationProfilesBody>
  >,
) {
  const { client, path, body, report } = context
  const { data: integration } = await sdk.getProjectIntegration({ client, path })
  await sdk.updateProjectIntegration({
    client,
    path,
    body: profileIntegrationProfileUpdate(integration, body.profile_ids),
  })
  report.info('Profiles saved. One launches immediately; several show a menu to select one.')
  report.done()
}

export const integrationCommandGroups: CommandGroup[] = [
  {
    name: 'integrations',
    summary: 'Manage project integrations, credentials and launchers',
    operations: [
      op({
        verb: 'definitions',
        summary: 'List available integration implementations and capabilities',
        fn: sdk.listIntegrationDefinitions,
        path: schemas.zListIntegrationDefinitionsPath,
        format: formatRecord(),
      }),
      op({
        verb: 'list',
        summary: 'List project integrations',
        fn: sdk.listProjectIntegrations,
        path: schemas.zListProjectIntegrationsPath,
        query: schemas.zListProjectIntegrationsQuery,
        format: formatTable(['id', 'name', 'integration_type', 'state']),
      }),
      op({
        verb: 'get',
        summary: 'Read integration setup and exported capabilities',
        fn: sdk.getProjectIntegration,
        path: schemas.zGetProjectIntegrationPath,
        format: formatRecord(),
      }),
      op({
        verb: 'create',
        summary: 'Create a disconnected integration and optional launcher',
        fn: sdk.createProjectIntegration,
        path: schemas.zCreateProjectIntegrationPath,
        body: schemas.zCreateProjectIntegrationBody,
        format: formatRecord(),
      }),
      op({
        verb: 'update',
        summary: 'Update launcher settings; integration name and implementation remain fixed',
        fn: sdk.updateProjectIntegration,
        path: schemas.zUpdateProjectIntegrationPath,
        body: schemas.zUpdateProjectIntegrationBody,
        format: formatRecord(),
      }),
      op({
        verb: 'configure',
        summary:
          'Verify GitHub or Discord credentials for an integration; use integrations slack for Slack',
        fn: sdk.configureProjectIntegration,
        path: schemas.zConfigureProjectIntegrationPath,
        body: schemas.zConfigureProjectIntegrationBody,
        format: formatRecord(),
      }),
      flowOp({
        verb: 'slack',
        summary: 'Connect this integration to Slack through browser authorization',
        path: schemas.zGetProjectIntegrationPath,
        body: zSlackBody,
        run: runSlackIntegration,
      }),
      flowOp({
        verb: 'profiles',
        summary: 'Edit offered Slack or Discord profiles',
        path: schemas.zGetProjectIntegrationPath,
        body: zIntegrationProfilesBody,
        run: runIntegrationProfilesUpdate,
      }),
      op({
        verb: 'disconnect',
        summary: 'Stop provider access while retaining integration setup and agents',
        fn: sdk.disconnectProjectIntegration,
        path: schemas.zDisconnectProjectIntegrationPath,
        format: formatRecord(),
      }),
      op({
        verb: 'delete',
        summary: 'Delete integration setup and revoke its capabilities; retain agents',
        fn: sdk.deleteProjectIntegration,
        path: schemas.zDeleteProjectIntegrationPath,
        format: formatVoid('deleted'),
      }),
    ],
  },
]
