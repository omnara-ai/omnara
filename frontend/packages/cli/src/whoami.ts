import type { Command } from 'commander'
import { sdk } from '@omnara/sdk'
import type { CliContext } from './context.ts'
import { renderResult, runCliAction } from './output.ts'

export function registerWhoami(program: Command, ctx: CliContext): void {
  program
    .command('whoami')
    .description('Show the authenticated user and their organizations')
    .option('--json', 'print the raw JSON response')
    .action(async (options: { json?: boolean }) => {
      await runCliAction(async () => {
        const { data } = await sdk.getCurrentUser({ client: ctx.client })
        renderResult(data, options.json === true)
      })
    })
}
