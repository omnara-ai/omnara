#!/usr/bin/env tsx
import { Command } from 'commander'
import { loadContext } from './context.ts'
import { registerGroup } from './factory.ts'

import { commandGroups } from './manifest.ts'
import { registerContextCommand } from './context.ts'
import { registerWhoami } from './whoami.ts'

const program = new Command('omnara')
  .description('Interact with the Omnara API')
  .option('--org <org-id>', 'organization ID for org-scoped commands (defaults to OMNARA_ORG_ID)')
  .option('--project <project-id>', 'project ID for project-scoped commands (defaults to OMNARA_PROJECT_ID)')
const ctx = loadContext()
for (const group of commandGroups) registerGroup(program, ctx, group)
registerWhoami(program, ctx)
registerContextCommand(program, ctx)
await program.parseAsync()
