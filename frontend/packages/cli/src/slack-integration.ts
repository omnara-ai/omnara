import {
  type CreateIntegrationOAuthSetupRequest,
  type CreateSlackSetupRequest,
  type IntegrationInstall,
  type IntegrationOAuthSetup,
  sdk,
  type SlackSetup,
} from '@omnara/sdk'
import { zCreateIntegrationOAuthSetupRequest, zCreateSlackSetupRequest } from '@omnara/sdk/zod'
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
  | { kind: 'existing-app'; body: CreateIntegrationOAuthSetupRequest }

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
    const result = zCreateIntegrationOAuthSetupRequest.safeParse({
      provider: 'slack',
      client_id: body.client_id,
      client_secret: body.client_secret,
      signing_secret: body.signing_secret,
      ...(body.return_to !== undefined ? { return_to: body.return_to } : {}),
    })
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
  const result = zCreateSlackSetupRequest.safeParse({
    app_name: body.app_name,
    app_configuration_token: body.app_configuration_token,
    ...(body.icon_data_base64 !== undefined
      ? {
          icon: {
            data_base64: body.icon_data_base64,
            ...(body.icon_filename !== undefined ? { filename: body.icon_filename } : {}),
          },
        }
      : {}),
    ...(body.return_to !== undefined ? { return_to: body.return_to } : {}),
  })
  if (!result.success) {
    throw new CliInputError(
      `invalid Slack app parameters:\n${z.prettifyError(result.error)}\nPass --app-name and --app-configuration-token, or existing app credentials.`,
    )
  }
  return { kind: 'create-app', body: result.data }
}

export async function runSlackIntegration(
  context: FlowContext<
    { orgID: string; projectID: string; agentProfileID: string },
    z.output<typeof zSlackBody>
  >,
): Promise<void> {
  const { client, path, body, report } = context
  const request = parseRequest(body)
  const setupPath = {
    orgID: path.orgID,
    projectID: path.projectID,
    agentProfileID: path.agentProfileID,
  }
  let start: IntegrationOAuthSetup | SlackSetup
  let slackAppId: string | undefined
  if (request.kind === 'create-app') {
    const { data } = await sdk.createSlackSetup({ client, path: setupPath, body: request.body })
    start = data
    slackAppId = data.slack_app_id
  } else {
    const { data } = await sdk.createIntegrationOAuthSetup({
      client,
      path: setupPath,
      body: request.body,
    })
    start = data
  }
  openAuthorizationUrl(
    report,
    'Approve the Slack integration in your browser',
    start.oauth_url,
    body.browser,
  )
  report.start('Waiting for the Slack integration to be created')
  let integration: IntegrationInstall
  try {
    integration = await pollUntilDeadline({
      expiresAt: start.expires_at,
      expiredMessage: 'Slack authorization expired before the integration was created',
      async fetchOnce() {
        const { data } = await sdk.listIntegrationInstalls({
          client,
          path: { orgID: path.orgID, projectID: path.projectID },
          query: { oauth_flow_id: start.flow_id, limit: 1 },
        })
        return data.data[0]
      },
    })
  } catch (error) {
    report.fail('Slack authorization failed')
    if (slackAppId !== undefined) {
      report.warn(`Slack app ${slackAppId} was created, but the integration was not completed`)
    }
    throw error
  }
  report.stop('Slack integration created')
  report.info(`Integration ID: ${integration.id}`)
  report.done()
}
