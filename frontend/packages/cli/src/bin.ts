#!/usr/bin/env tsx
import { Command } from 'commander'

import { loadContext, registerContextCommand } from './context.ts'
import { registerGroup } from './factory.ts'
import { commandGroups } from './manifest.ts'
import { registerWhoami } from './whoami.ts'

process.stdout.on('error', (error: NodeJS.ErrnoException) => {
  if (error.code === 'EPIPE') process.exit(0)
  throw error
})

const program = new Command('omnara').description('Interact with the Omnara API')
const ctx = loadContext()
for (const group of commandGroups) registerGroup(program, ctx, group)
registerWhoami(program, ctx)
registerContextCommand(program, ctx)
await program.parseAsync()
