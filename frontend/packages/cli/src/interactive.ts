import { isCancel, select } from '@clack/prompts'
import { sdk, type OmnaraClient } from '@omnara/sdk'
import { CliInputError } from './output.ts'

export function canPromptInteractively(): boolean {
  return process.stdin.isTTY === true && process.stdout.isTTY === true
}

interface Choice {
  id: string
  label: string
}

async function selectFrom(message: string, choices: Choice[]): Promise<string> {
  if (choices.length === 1) {
    const only = choices[0] as Choice
    console.log(`${message}: ${only.label} (only option)`)
    return only.id
  }
  const picked = await select({
    message,
    options: choices.map((choice) => ({ value: choice.id, label: choice.label, hint: choice.id })),
  })
  if (isCancel(picked)) throw new CliInputError('selection cancelled')
  return picked
}

export async function promptOrgSelection(client: OmnaraClient): Promise<string> {
  const { data } = await sdk.getCurrentUser({ client })
  if (data.orgs.length === 0) {
    throw new CliInputError('this account has no organizations')
  }
  return selectFrom(
    'Select an organization',
    data.orgs.map((org) => ({ id: org.id, label: org.name })),
  )
}

export async function promptProjectSelection(
  client: OmnaraClient,
  orgId: string,
): Promise<string> {
  const projects: Choice[] = []
  let cursor: string | undefined
  do {
    const { data } = await sdk.listVisibleProjects({
      client,
      path: { orgID: orgId },
      query: cursor === undefined ? {} : { cursor },
    })
    projects.push(...data.data.map((project) => ({ id: project.id, label: project.name })))
    cursor = data.next_cursor ?? undefined
  } while (cursor !== undefined)
  if (projects.length === 0) {
    throw new CliInputError(`no projects visible in organization ${orgId}`)
  }
  return selectFrom('Select a project', projects)
}
