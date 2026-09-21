import { profileAppProfileUpdate, sdk } from '@omnara/sdk'
import * as schemas from '@omnara/sdk/zod'
import * as z from 'zod'

import { type CommandGroup, type FlowContext, flowOp, op } from './factory.ts'
import { formatRecord, formatTable, formatVoid } from './format.ts'
import { runSlackIntegration, zSlackBody } from './slack-integration.ts'

export const zAppProfilesBody = z.object({
  profile_ids: z
    .array(schemas.zAgentProfileId)
    .max(16)
    .describe('repeat for each offered profile; keeps existing-agent slots and other app settings'),
})

export async function runAppProfilesUpdate(
  context: FlowContext<
    z.output<typeof schemas.zGetProjectAppPath>,
    z.output<typeof zAppProfilesBody>
  >,
) {
  const { client, path, body, report } = context
  const { data: app } = await sdk.getProjectApp({ client, path })
  await sdk.updateProjectApp({ client, path, body: profileAppProfileUpdate(app, body.profile_ids) })
  report.info('Profiles saved. One launches immediately; several show a menu to select one.')
  report.done()
}

export const appCommandGroups: CommandGroup[] = [
  {
    name: 'apps',
    summary: 'Manage project apps, credentials and launchers',
    operations: [
      op({
        verb: 'definitions',
        summary: 'List available app implementations and capabilities',
        fn: sdk.listAppDefinitions,
        path: schemas.zListAppDefinitionsPath,
        format: formatRecord(),
      }),
      op({
        verb: 'list',
        summary: 'List project apps',
        fn: sdk.listProjectApps,
        path: schemas.zListProjectAppsPath,
        query: schemas.zListProjectAppsQuery,
        format: formatTable(['id', 'name', 'app_type', 'state']),
      }),
      op({
        verb: 'get',
        summary: 'Read app setup and exported capabilities',
        fn: sdk.getProjectApp,
        path: schemas.zGetProjectAppPath,
        format: formatRecord(),
      }),
      op({
        verb: 'create',
        summary: 'Create a disconnected app and optional launcher',
        fn: sdk.createProjectApp,
        path: schemas.zCreateProjectAppPath,
        body: schemas.zCreateProjectAppBody,
        format: formatRecord(),
      }),
      op({
        verb: 'update',
        summary: 'Update launcher settings; app name and implementation remain fixed',
        fn: sdk.updateProjectApp,
        path: schemas.zUpdateProjectAppPath,
        body: schemas.zUpdateProjectAppBody,
        format: formatRecord(),
      }),
      op({
        verb: 'configure',
        summary: 'Verify GitHub or Discord credentials for an app; use apps slack for Slack',
        fn: sdk.configureProjectApp,
        path: schemas.zConfigureProjectAppPath,
        body: schemas.zConfigureProjectAppBody,
        format: formatRecord(),
      }),
      flowOp({
        verb: 'slack',
        summary: 'Connect this app to Slack through browser authorization',
        path: schemas.zGetProjectAppPath,
        body: zSlackBody,
        run: runSlackIntegration,
      }),
      flowOp({
        verb: 'profiles',
        summary: 'Edit offered Slack or Discord profiles',
        path: schemas.zGetProjectAppPath,
        body: zAppProfilesBody,
        run: runAppProfilesUpdate,
      }),
      op({
        verb: 'disconnect',
        summary: 'Stop provider access while retaining app setup and agents',
        fn: sdk.disconnectProjectApp,
        path: schemas.zDisconnectProjectAppPath,
        format: formatRecord(),
      }),
      op({
        verb: 'delete',
        summary: 'Delete app setup and revoke its capabilities; retain agents',
        fn: sdk.deleteProjectApp,
        path: schemas.zDeleteProjectAppPath,
        format: formatVoid('deleted'),
      }),
    ],
  },
]
