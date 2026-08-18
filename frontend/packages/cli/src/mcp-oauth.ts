import {
  type McpoAuthStartRequest,
  type OmnaraClient,
  sdk,
  type Secret,
  type SecretOwnerInput,
} from '@omnara/sdk'
import { zMcpoAuthStartRequest } from '@omnara/sdk/zod'
import * as z from 'zod'

import { openInBrowser } from './browser.ts'
import type { FlowContext } from './factory.ts'
import { CliInputError } from './output.ts'
import type { FlowReporter } from './reporter.ts'

const POLL_INTERVAL_SECONDS = 2

type SecretSnapshot = Map<string, { version: number; updatedAt: string }>

function exactGlob(value: string): string {
  return value.replace(/[\\*?]/g, '\\$&')
}

function sameOwner(secret: Secret, owner: SecretOwnerInput): boolean {
  if (secret.owner.kind !== owner.kind) return false
  if (owner.kind !== 'project') return true
  return secret.owner.kind === 'project' && secret.owner.project_id === owner.project_id
}

async function matchingSecrets(
  client: OmnaraClient,
  orgId: string,
  owner: SecretOwnerInput,
  name: string,
): Promise<Secret[]> {
  const { data } = await sdk.listSecrets({
    client,
    path: { orgID: orgId },
    query: {
      name: exactGlob(name),
      kind: 'oauth_token_set',
      owner_kind: owner.kind,
      ...(owner.kind === 'project' ? { owner_project_id: owner.project_id } : {}),
      limit: 100,
    },
  })
  return data.data.filter((secret) => secret.name === name && sameOwner(secret, owner))
}

async function snapshotSecrets(
  client: OmnaraClient,
  orgId: string,
  owner: SecretOwnerInput,
  name: string,
): Promise<SecretSnapshot> {
  return new Map(
    (await matchingSecrets(client, orgId, owner, name)).map((secret) => [
      secret.id,
      { version: secret.current_version_number, updatedAt: secret.updated_at },
    ]),
  )
}

function changedSecret(secrets: Secret[], before: SecretSnapshot): Secret | undefined {
  return secrets.find((secret) => {
    const previous = before.get(secret.id)
    return (
      previous === undefined ||
      secret.current_version_number > previous.version ||
      secret.updated_at !== previous.updatedAt
    )
  })
}

function defaultSleep(seconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, seconds * 1000))
}

async function pollForMcpOAuthSecret(options: {
  client: OmnaraClient
  orgId: string
  owner: SecretOwnerInput
  name: string
  before: SecretSnapshot
  expiresAt: string
  sleep?: (seconds: number) => Promise<void>
  now?: () => number
}): Promise<Secret> {
  const sleep = options.sleep ?? defaultSleep
  const now = options.now ?? Date.now
  const deadline = Date.parse(options.expiresAt)
  while (now() < deadline) {
    const created = changedSecret(
      await matchingSecrets(options.client, options.orgId, options.owner, options.name),
      options.before,
    )
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
  const before = await snapshotSecrets(
    options.client,
    options.orgId,
    options.request.owner,
    options.request.name,
  )
  const { data: start } = await sdk.startSecretMcpoAuth({
    client: options.client,
    path: { orgID: options.orgId },
    body: options.request,
  })
  options.onAuthorization(start.authorization_url)
  return pollForMcpOAuthSecret({
    client: options.client,
    orgId: options.orgId,
    owner: options.request.owner,
    name: options.request.name,
    before,
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
