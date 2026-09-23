import {
  type AppOAuthSetup,
  type CreateAppOAuthSetupRequest,
  type CreateSlackSetupRequest,
  type ProjectApp,
  sdk,
  type SlackSetup,
} from '@omnara/sdk'
import { zCreateAppOAuthSetupRequest, zCreateSlackSetupRequest } from '@omnara/sdk/zod'
import * as z from 'zod'

import type { FlowContext } from './factory.ts'
import { openAuthorizationUrl, zBrowserFlag } from './mcp-oauth.ts'
import { CliInputError } from './output.ts'
import { pollUntilDeadline } from './poll.ts'

export const zSlackBody = z.object({
  app_name: z.string().optional().describe('name for a new Slack app'),
  app_configuration_token: z
    .string()
    .optional()
    .describe('Slack app configuration token for a new app'),
  icon_filename: z.string().optional().describe('filename for the new Slack app icon'),
  icon_data_base64: z.string().optional().describe('base64-encoded Slack app icon'),
  client_id: z.string().optional().describe('existing Slack app client ID'),
  client_secret: z.string().optional().describe('existing Slack app client secret'),
  signing_secret: z.string().optional().describe('existing Slack app signing secret'),
  return_to: z.string().optional().describe('console-relative path opened after authorization'),
  browser: zBrowserFlag,
})

type SlackBody = z.output<typeof zSlackBody>

type SlackSetupRequest =
  | { kind: 'create-app'; body: CreateSlackSetupRequest }
  | { kind: 'existing-app'; body: CreateAppOAuthSetupRequest }

function parseRequest(body: SlackBody): SlackSetupRequest {
  const createAppFields = [
    body.app_name,
    body.app_configuration_token,
    body.icon_filename,
    body.icon_data_base64,
  ]
  const existingAppFields = [body.client_id, body.client_secret, body.signing_secret]
  const createsApp = createAppFields.some((value) => value !== undefined)
  const usesExistingApp = existingAppFields.some((value) => value !== undefined)
  if (createsApp && usesExistingApp) {
    throw new CliInputError(
      'choose either --app-name with --app-configuration-token, or existing app credentials',
    )
  }

  if (usesExistingApp) {
    const draft: Partial<z.input<typeof zCreateAppOAuthSetupRequest>> = {
      client_id: body.client_id,
      client_secret: body.client_secret,
      signing_secret: body.signing_secret,
    }
    if (body.return_to !== undefined) draft.return_to = body.return_to
    const result = zCreateAppOAuthSetupRequest.safeParse(draft)
    if (!result.success) {
      throw new CliInputError(
        `invalid existing Slack app parameters:\n${z.prettifyError(result.error)}`,
      )
    }
    return { kind: 'existing-app', body: result.data }
  }

  if (body.icon_filename !== undefined && body.icon_data_base64 === undefined) {
    throw new CliInputError('--icon-filename requires --icon-data-base64')
  }
  const draft: Partial<z.input<typeof zCreateSlackSetupRequest>> = {
    app_name: body.app_name,
    app_configuration_token: body.app_configuration_token,
  }
  if (body.icon_data_base64 !== undefined) {
    draft.icon = { data_base64: body.icon_data_base64 }
    if (body.icon_filename !== undefined) draft.icon.filename = body.icon_filename
  }
  if (body.return_to !== undefined) draft.return_to = body.return_to
  const result = zCreateSlackSetupRequest.safeParse(draft)
  if (!result.success) {
    throw new CliInputError(
      `invalid Slack app parameters:\n${z.prettifyError(result.error)}\nPass --app-name and --app-configuration-token, or existing app credentials.`,
    )
  }
  return { kind: 'create-app', body: result.data }
}

export async function runSlackApp(
  context: FlowContext<
    { orgID: string; projectID: string; appID: string },
    z.output<typeof zSlackBody>
  >,
): Promise<void> {
  const { client, path, body, report } = context
  const request = parseRequest(body)
  const setupPath = {
    orgID: path.orgID,
    projectID: path.projectID,
    appID: path.appID,
  }
  let start: AppOAuthSetup | SlackSetup
  let slackAppId: string | undefined
  if (request.kind === 'create-app') {
    const { data } = await sdk.createProjectAppSlackSetup({
      client,
      path: setupPath,
      body: request.body,
    })
    start = data
    slackAppId = data.slack_app_id
  } else {
    const { data } = await sdk.createProjectAppOAuthSetup({
      client,
      path: setupPath,
      body: request.body,
    })
    start = data
  }
  openAuthorizationUrl(
    report,
    'Authorize the Slack app in your browser',
    start.oauth_url,
    body.browser,
  )
  report.start('Waiting for the Slack app setup to be saved')
  let app: ProjectApp
  try {
    app = await pollUntilDeadline({
      expiresAt: start.expires_at,
      expiredMessage: 'Slack authorization expired before app setup were saved',
      async fetchOnce() {
        const { data } = await sdk.getProjectApp({ client, path: setupPath })
        return data.state === 'active' && data.last_oauth_flow_id === start.flow_id
          ? data
          : undefined
      },
    })
  } catch (error) {
    report.fail('Slack authorization failed')
    if (slackAppId !== undefined) {
      report.warn(`Slack app ${slackAppId} was created, but Omnara setup was not completed`)
    }
    throw error
  }
  report.stop('Slack app setup saved')
  report.info(`App ID: ${app.id} (${app.name})`)
  report.info(
    'Reconnect preserves existing app settings. Use apps get/update to inspect or change the launcher.',
  )
  report.done()
}
