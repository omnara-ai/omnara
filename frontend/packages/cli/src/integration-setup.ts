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
    .describe('repeat for each offered profile; preserves other integration settings'),
})

export async function runIntegrationProfilesUpdate(
  context: FlowContext<
    z.output<typeof schemas.zGetIntegrationPath>,
    z.output<typeof zIntegrationProfilesBody>
  >,
) {
  const { client, path, body, report } = context
  const { data: integration } = await sdk.getIntegration({ client, path })
  await sdk.updateIntegration({
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
        fn: sdk.listIntegrations,
        path: schemas.zListIntegrationsPath,
        query: schemas.zListIntegrationsQuery,
        format: formatTable(['id', 'name', 'integration_kind', 'state']),
      }),
      op({
        verb: 'get',
        summary: 'Read integration setup and exported capabilities',
        fn: sdk.getIntegration,
        path: schemas.zGetIntegrationPath,
        format: formatRecord(),
      }),
      op({
        verb: 'create',
        summary: 'Create a disconnected integration and optional launcher',
        fn: sdk.createIntegration,
        path: schemas.zCreateIntegrationPath,
        body: schemas.zCreateIntegrationBody,
        format: formatRecord(),
      }),
      op({
        verb: 'update',
        summary: 'Replace integration settings; name and kind are immutable',
        fn: sdk.updateIntegration,
        path: schemas.zUpdateIntegrationPath,
        body: schemas.zUpdateIntegrationBody,
        format: formatRecord(),
      }),
      op({
        verb: 'configure',
        summary:
          'Verify GitHub or Discord credentials for an integration; use integrations slack for Slack',
        fn: sdk.configureIntegration,
        path: schemas.zConfigureIntegrationPath,
        body: schemas.zConfigureIntegrationBody,
        format: formatRecord(),
      }),
      flowOp({
        verb: 'slack',
        summary: 'Connect this integration to Slack through browser authorization',
        path: schemas.zGetIntegrationPath,
        body: zSlackBody,
        run: runSlackIntegration,
      }),
      flowOp({
        verb: 'profiles',
        summary: 'Edit offered Slack or Discord profiles',
        path: schemas.zGetIntegrationPath,
        body: zIntegrationProfilesBody,
        run: runIntegrationProfilesUpdate,
      }),
      op({
        verb: 'disconnect',
        summary: 'Stop provider access while retaining integration setup and agents',
        fn: sdk.disconnectIntegration,
        path: schemas.zDisconnectIntegrationPath,
        format: formatRecord(),
      }),
      op({
        verb: 'delete',
        summary: 'Delete integration setup and revoke its capabilities; retain agents',
        fn: sdk.deleteIntegration,
        path: schemas.zDeleteIntegrationPath,
        format: formatVoid('deleted'),
      }),
    ],
  },
]
