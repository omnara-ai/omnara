import type { Command } from 'commander'

import type { CliConfig } from './config.ts'
import { configFilePath, readConfigFile, updateConfigFile } from './config-file.ts'
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
        const { orgId } = await loginWithDevice({
          apiUrl: cli.apiUrl,
          issuerUrl: cli.issuerUrl,
          browser: options.browser,
          tokenName: options.tokenName,
          report,
        })
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
