import { type McpoAuthStartRequest, type OmnaraClient, sdk, type Secret } from '@omnara/sdk'
import { zMcpoAuthStartRequest } from '@omnara/sdk/zod'
import * as z from 'zod'

import { openInBrowser } from './browser.ts'
import type { FlowContext } from './factory.ts'
import { CliInputError } from './output.ts'
import type { FlowReporter } from './reporter.ts'

const POLL_INTERVAL_SECONDS = 2

function defaultSleep(seconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, seconds * 1000))
}

async function pollForMcpOAuthSecret(options: {
  client: OmnaraClient
  orgId: string
  flowId: string
  expiresAt: string
  sleep?: (seconds: number) => Promise<void>
  now?: () => number
}): Promise<Secret> {
  const sleep = options.sleep ?? defaultSleep
  const now = options.now ?? Date.now
  const deadline = Date.parse(options.expiresAt)
  while (now() < deadline) {
    const { data } = await sdk.listSecrets({
      client: options.client,
      path: { orgID: options.orgId },
      query: { mcp_oauth_flow_id: options.flowId, limit: 1 },
    })
    const [created] = data.data
    if (created !== undefined) return created
    await sleep(Math.min(POLL_INTERVAL_SECONDS, Math.max(0, (deadline - now()) / 1000)))
  }
  throw new CliInputError('MCP OAuth authorization expired before the secret was created')
}

export async function authorizeMcpOAuthSecret(options: {
  client: OmnaraClient
  orgId: string
  request: McpoAuthStartRequest
  onAuthorization: (url: string) => void
}): Promise<Secret> {
  const { data: start } = await sdk.startSecretMcpoAuth({
    client: options.client,
    path: { orgID: options.orgId },
    body: options.request,
  })
  options.onAuthorization(start.authorization_url)
  return pollForMcpOAuthSecret({
    client: options.client,
    orgId: options.orgId,
    flowId: start.flow_id,
    expiresAt: start.expires_at,
  })
}

export const zBrowserFlag = z
  .boolean()
  .default(true)
  .describe('open the authorization URL in a browser')

export function openAuthorizationUrl(
  report: FlowReporter,
  title: string,
  url: string,
  browser: boolean,
): void {
  report.url(title, url)
  if (browser) {
    openInBrowser(url, (message) => {
      report.warn(message)
    })
  }
}

export const zMcpOAuthBody = zMcpoAuthStartRequest.extend({ browser: zBrowserFlag })

export async function runMcpOAuth(
  context: FlowContext<{ orgID: string }, z.output<typeof zMcpOAuthBody>>,
): Promise<void> {
  const { client, path, body, report } = context
  const { browser, ...request } = body
  let secret: Secret
  try {
    secret = await authorizeMcpOAuthSecret({
      client,
      orgId: path.orgID,
      request,
      onAuthorization(url) {
        openAuthorizationUrl(report, 'Authorize this MCP server in your browser', url, browser)
        report.start('Waiting for the secret to be created')
      },
    })
  } catch (error) {
    report.fail('Authorization failed')
    throw error
  }
  report.stop('Secret created')
  report.info(`Secret ID: ${secret.id}`)
  report.done()
}
