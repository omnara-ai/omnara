import { hostname } from 'node:os'

import { intro, log, note, outro, spinner } from '@clack/prompts'
import {
  bearerToken,
  cliLoginTokenName,
  createOmnaraClient,
  OMNARA_CLI_OAUTH_CLIENT_ID,
  type OmnaraClient,
  pollDeviceAuthToken,
  sdk,
  startDeviceAuth,
} from '@omnara/sdk'
import { zResourceName } from '@omnara/sdk/zod'
import * as z from 'zod'

import { openInBrowser } from './browser.ts'
import { configFilePath, updateConfigFile } from './config-file.ts'
import { canPromptInteractively } from './interactive.ts'
import { CliInputError } from './output.ts'

async function isProjectVisible(
  client: OmnaraClient,
  orgId: string,
  projectId: string,
): Promise<boolean> {
  let cursor: string | undefined
  do {
    const { data } = await sdk.listVisibleProjects({
      client,
      path: { orgID: orgId },
      query: cursor === undefined ? {} : { cursor },
    })
    if (data.data.some((project) => project.id === projectId)) return true
    cursor = data.next_cursor ?? undefined
  } while (cursor !== undefined)
  return false
}

export interface LoginReporter {
  showCode(userCode: string, approvalUrl: string): void
  startWaiting(): void
  stopWaiting(approved: boolean): void
  success(message: string): void
  info(message: string): void
  warn(message: string): void
  finish(message?: string): void
}

function interactiveReporter(title: string): LoginReporter {
  intro(title)
  const spin = spinner()
  return {
    showCode(userCode, approvalUrl) {
      note(`Code: ${userCode}\n${approvalUrl}`, 'Confirm this code in your browser')
    },
    startWaiting() {
      spin.start('Waiting for approval in your browser')
    },
    stopWaiting(approved) {
      if (approved) {
        spin.stop('Approved')
      } else {
        spin.error('Approval failed')
      }
    },
    success(message) {
      log.success(message)
    },
    info(message) {
      log.info(message)
    },
    warn(message) {
      log.warn(message)
    },
    finish(message) {
      outro(message ?? 'Done')
    },
  }
}

function plainReporter(title: string): LoginReporter {
  console.log(title)
  return {
    showCode(userCode, approvalUrl) {
      console.log(`Confirm this code in your browser: ${userCode}`)
      console.log(`Approval page: ${approvalUrl}`)
    },
    startWaiting() {
      console.log('Waiting for approval...')
    },
    stopWaiting(approved) {
      if (approved) console.log('Approved.')
    },
    success(message) {
      console.log(message)
    },
    info(message) {
      console.log(message)
    },
    warn(message) {
      console.error(`warning: ${message}`)
    },
    finish(message) {
      if (message !== undefined) console.log(message)
    },
  }
}

export function createLoginReporter(title: string): LoginReporter {
  return canPromptInteractively() ? interactiveReporter(title) : plainReporter(title)
}

export function loginTokenName(explicit: string | undefined, hostName: string): string {
  const candidate = explicit ?? cliLoginTokenName(hostName)
  const result = zResourceName.safeParse(candidate)
  if (result.success) return result.data
  if (explicit === undefined) return cliLoginTokenName()
  throw new CliInputError(`invalid token name:\n${z.prettifyError(result.error)}`)
}

export interface DeviceLoginOptions {
  apiUrl: string
  issuerUrl: string
  browser: boolean
  tokenName?: string
  report: LoginReporter
}

export interface DeviceLoginResult {
  token: string
  orgId?: string
}

export async function loginWithDevice(options: DeviceLoginOptions): Promise<DeviceLoginResult> {
  const { report } = options
  updateConfigFile({})
  const start = await startDeviceAuth({
    issuerUrl: options.issuerUrl,
    clientId: OMNARA_CLI_OAUTH_CLIENT_ID,
    tokenName: loginTokenName(options.tokenName, hostname()),
  })
  report.showCode(start.userCode, start.verificationUriComplete)
  if (options.browser) {
    openInBrowser(start.verificationUriComplete, (message) => {
      report.warn(message)
    })
  }
  report.startWaiting()
  let token: string
  try {
    token = await pollDeviceAuthToken({
      tokenEndpoint: start.tokenEndpoint,
      clientId: start.clientId,
      deviceCode: start.deviceCode,
      intervalSeconds: start.intervalSeconds,
    })
  } catch (error) {
    report.stopWaiting(false)
    throw error
  }
  report.stopWaiting(true)
  const saved = updateConfigFile({
    token,
    api_url: options.apiUrl,
    issuer_url: options.issuerUrl,
  })
  const client = createOmnaraClient({ baseUrl: options.apiUrl, auth: bearerToken(token) })
  let orgId = saved.org_id
  let loginMessage = 'Logged in'
  let validationWarning: string | undefined
  try {
    const { data: me } = await sdk.getCurrentUser({ client })
    loginMessage = `Logged in as ${me.user.email || me.user.display_name}`
    if (orgId !== undefined && !me.orgs.some((org) => org.id === orgId)) {
      updateConfigFile({ org_id: undefined, project_id: undefined })
      orgId = undefined
      report.warn(
        'Cleared the saved default organization and project: this account cannot access them.',
      )
    } else if (
      orgId !== undefined &&
      saved.project_id !== undefined &&
      !(await isProjectVisible(client, orgId, saved.project_id))
    ) {
      updateConfigFile({ project_id: undefined })
      report.warn('Cleared the saved default project: this account cannot access it.')
    }
  } catch (error) {
    validationWarning = `could not verify the account or saved organization and project defaults: ${error instanceof Error ? error.message : String(error)}`
  }
  report.success(loginMessage)
  report.info(`Token saved to ${configFilePath()}`)
  if (validationWarning !== undefined) report.warn(validationWarning)
  if (process.env.OMNARA_API_KEY !== undefined) {
    report.warn('OMNARA_API_KEY is set and takes precedence over the saved token')
  }
  return { token, orgId }
}
