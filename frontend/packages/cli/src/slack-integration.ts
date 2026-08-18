import {
  type CreateIntegrationOAuthSetupRequest,
  type CreateSlackSetupRequest,
  type IntegrationInstall,
  type OmnaraClient,
  sdk,
} from '@omnara/sdk'
import { zCreateIntegrationOAuthSetupRequest, zCreateSlackSetupRequest } from '@omnara/sdk/zod'
import * as z from 'zod'

import type { FlowContext } from './factory.ts'
import { openAuthorizationUrl, zBrowserFlag } from './mcp-oauth.ts'
import { CliInputError } from './output.ts'

const POLL_INTERVAL_SECONDS = 2

type InstallSnapshot = Map<string, { state: string; updatedAt: string }>

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

async function profileSlackIntegrations(
  client: OmnaraClient,
  orgId: string,
  projectId: string,
  profileId: string,
): Promise<IntegrationInstall[]> {
  const { data } = await sdk.listIntegrationInstalls({
    client,
    path: { orgID: orgId, projectID: projectId },
    query: { agent_profile_id: profileId, limit: 100 },
  })
  return data.data.filter(
    (install) => install.provider === 'slack' && install.agent_profile_id === profileId,
  )
}

async function snapshotIntegrations(
  client: OmnaraClient,
  orgId: string,
  projectId: string,
  profileId: string,
): Promise<InstallSnapshot> {
  return new Map(
    (await profileSlackIntegrations(client, orgId, projectId, profileId)).map((install) => [
      install.id,
      { state: install.state, updatedAt: install.updated_at },
    ]),
  )
}

function changedIntegration(
  integrations: IntegrationInstall[],
  before: InstallSnapshot,
  expectedProviderAccountRef?: string,
): IntegrationInstall | undefined {
  return integrations.find((integration) => {
    if (
      expectedProviderAccountRef !== undefined &&
      integration.provider_account_ref !== expectedProviderAccountRef
    ) {
      return false
    }
    const previous = before.get(integration.id)
    if (previous === undefined) return true
    return integration.state !== previous.state || integration.updated_at !== previous.updatedAt
  })
}

function defaultSleep(seconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, seconds * 1000))
}

async function pollForSlackIntegration(options: {
  client: OmnaraClient
  orgId: string
  projectId: string
  profileId: string
  before: InstallSnapshot
  expiresAt: string
  expectedProviderAccountRef?: string
  sleep?: (seconds: number) => Promise<void>
  now?: () => number
}): Promise<IntegrationInstall> {
  const sleep = options.sleep ?? defaultSleep
  const now = options.now ?? Date.now
  const deadline = Date.parse(options.expiresAt)
  while (now() < deadline) {
    const created = changedIntegration(
      await profileSlackIntegrations(
        options.client,
        options.orgId,
        options.projectId,
        options.profileId,
      ),
      options.before,
      options.expectedProviderAccountRef,
    )
    if (created !== undefined) return created
    await sleep(Math.min(POLL_INTERVAL_SECONDS, Math.max(0, (deadline - now()) / 1000)))
  }
  throw new CliInputError('Slack authorization expired before the integration was created')
}

export async function runSlackIntegration(
  context: FlowContext<
    { orgID: string; projectID: string; agentProfileID: string },
    z.output<typeof zSlackBody>
  >,
): Promise<void> {
  const { client, path, body, report } = context
  const request = parseRequest(body)
  const before = await snapshotIntegrations(client, path.orgID, path.projectID, path.agentProfileID)
  const setupPath = {
    orgID: path.orgID,
    projectID: path.projectID,
    agentProfileID: path.agentProfileID,
  }
  const start =
    request.kind === 'create-app'
      ? await sdk.createSlackSetup({ client, path: setupPath, body: request.body })
      : await sdk.createIntegrationOAuthSetup({ client, path: setupPath, body: request.body })
  const providerAccountRef = 'slack_app_id' in start.data ? start.data.slack_app_id : undefined
  openAuthorizationUrl(
    report,
    'Approve the Slack integration in your browser',
    start.data.oauth_url,
    body.browser,
  )
  report.start('Waiting for the Slack integration to be created')
  let integration: IntegrationInstall
  try {
    integration = await pollForSlackIntegration({
      client,
      orgId: path.orgID,
      projectId: path.projectID,
      profileId: path.agentProfileID,
      before,
      expiresAt: start.data.expires_at,
      ...(typeof providerAccountRef === 'string'
        ? { expectedProviderAccountRef: providerAccountRef }
        : {}),
    })
  } catch (error) {
    report.fail('Slack authorization failed')
    throw error
  }
  report.stop('Slack integration created')
  report.info(`Integration ID: ${integration.id}`)
  report.done()
}
