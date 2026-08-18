#!/usr/bin/env tsx
import { Command } from 'commander'

import { loadConfig, registerConfigCommand } from './config.ts'
import { registerGroup, registerOperation } from './factory.ts'
import { commandGroups, topLevelOperations } from './manifest.ts'

process.stdout.on('error', (error: NodeJS.ErrnoException) => {
  if (error.code === 'EPIPE') process.exit(0)
  throw error
})

const program = new Command('omnara').description('Interact with the Omnara API')
program.configureHelp({
  subcommandTerm: (cmd) => {
    const args = cmd.registeredArguments
      .map((arg) =>
        arg.required
          ? `<${arg.name()}${arg.variadic ? '...' : ''}>`
          : `[${arg.name()}${arg.variadic ? '...' : ''}]`,
      )
      .join(' ')
    return [cmd.name(), cmd.options.length > 0 ? '[options]' : '', args]
      .filter((part) => part !== '')
      .join(' ')
  },
})
const config = loadConfig()
for (const group of commandGroups) registerGroup(program, config, group)
for (const operation of topLevelOperations) registerOperation(program, config, operation)
registerConfigCommand(program, config)
await program.parseAsync()
