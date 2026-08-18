import { spawn } from 'node:child_process'
import { hostname } from 'node:os'

import { intro, log, note, outro, spinner } from '@clack/prompts'
import {
  bearerToken,
  createOmnaraClient,
  type OmnaraClient,
  pollDeviceAuthToken,
  sdk,
  startDeviceAuth,
} from '@omnara/sdk'
import type { Command } from 'commander'

import { type CliConfig, configFilePath, readConfigFile, updateConfigFile } from './config.ts'
import { canPromptInteractively } from './interactive.ts'
import { runCliAction } from './output.ts'

function browserCommand(url: string): { command: string; args: string[] } {
  if (process.platform === 'darwin') return { command: 'open', args: [url] }
  if (process.platform === 'win32') return { command: 'cmd', args: ['/c', 'start', '', url] }
  return { command: 'xdg-open', args: [url] }
}

function openInBrowser(url: string, onError: (message: string) => void): void {
  const { command, args } = browserCommand(url)
  const child = spawn(command, args, { stdio: 'ignore', detached: true })
  child.once('error', () => {
    onError('could not open a browser; use the approval page URL above')
  })
  child.unref()
}

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

interface LoginReporter {
  showCode(userCode: string, approvalUrl: string): void
  startWaiting(): void
  stopWaiting(approved: boolean): void
  success(message: string): void
  info(message: string): void
  warn(message: string): void
  finish(message?: string): void
}

function interactiveReporter(baseUrl: string): LoginReporter {
  intro(`Log in to ${baseUrl}`)
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

function plainReporter(): LoginReporter {
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

interface LoginOptions {
  browser: boolean
  tokenName?: string
}

export function registerLoginCommand(program: Command, cli: CliConfig): void {
  program
    .command('login')
    .description('Log in via the browser and save an API token')
    .option('--no-browser', 'print the approval URL instead of opening a browser')
    .option('--token-name <name>', 'name for the created API token')
    .action(async (options: LoginOptions) => {
      await runCliAction(async () => {
        const report = canPromptInteractively() ? interactiveReporter(cli.baseUrl) : plainReporter()
        const start = await startDeviceAuth({
          baseUrl: cli.baseUrl,
          clientName: 'Omnara CLI',
          tokenName: options.tokenName ?? `Omnara CLI on ${hostname()}`,
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
            baseUrl: cli.baseUrl,
            deviceCode: start.deviceCode,
            intervalSeconds: start.intervalSeconds,
          })
        } catch (error) {
          report.stopWaiting(false)
          throw error
        }
        report.stopWaiting(true)
        const saved = updateConfigFile({ token, base_url: cli.baseUrl })
        const client = createOmnaraClient({ baseUrl: cli.baseUrl, auth: bearerToken(token) })
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
        if ((process.env.OMNARA_ORG_ID ?? orgId) === undefined) {
          report.finish("Run 'omnara config select' to choose a default organization and project.")
        } else {
          report.finish()
        }
      })
    })
  program
    .command('logout')
    .description('Remove the saved API token')
    .action(async () => {
      await runCliAction(() => {
        if (readConfigFile().token === undefined) {
          console.log('No saved token.')
          return
        }
        updateConfigFile({ token: undefined })
        console.log(
          `Removed the saved token from ${configFilePath()}. It stays valid until revoked on the API Tokens page.`,
        )
      })
    })
}
