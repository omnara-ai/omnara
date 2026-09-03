import type { Command } from 'commander'

import type { CliConfig } from './config.ts'
import { createLoginReporter, loginWithDevice } from './device-login.ts'
import { runCliAction } from './output.ts'

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
        const report = createLoginReporter(`Log in to ${cli.issuerUrl}`)
        const { orgId, hasOrganizations } = await loginWithDevice({
          apiUrl: cli.apiUrl,
          issuerUrl: cli.issuerUrl,
          browser: options.browser,
          tokenName: options.tokenName,
          report,
          store: cli.store,
          fetch: cli.fetch,
          sleep: cli.sleep,
        })
        if (hasOrganizations === false) {
          report.finish(
            `This account has no organization yet. Create one at ${new URL('/onboarding', cli.issuerUrl).toString()}, then run 'omnara config select'.`,
          )
        } else if ((process.env.OMNARA_ORG_ID ?? orgId) === undefined) {
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
        if (cli.store.read().token === undefined) {
          console.log('No saved token.')
          return
        }
        cli.store.update({ token: undefined })
        console.log(
          `Removed the saved token from ${cli.store.path}. It stays valid until revoked on the API Tokens page.`,
        )
      })
    })
}
