import { isCancel, select } from '@clack/prompts'
import { type OmnaraClient, sdk } from '@omnara/sdk'

import { CliInputError } from './output.ts'

export function canPromptInteractively(): boolean {
  return process.stdin.isTTY && process.stdout.isTTY
}

interface Choice {
  id: string
  label: string
}

async function selectFrom(message: string, choices: Choice[]): Promise<string> {
  const [only] = choices
  if (choices.length === 1 && only !== undefined) {
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

export async function promptOrgSelection(client: OmnaraClient, issuerUrl: string): Promise<string> {
  const { data } = await sdk.getCurrentUser({ client })
  if (data.orgs.length === 0) {
    throw new CliInputError(
      `this account has no organizations; create one at ${new URL('/onboarding', issuerUrl).toString()}`,
    )
  }
  return selectFrom(
    'Select an organization',
    data.orgs.map((org) => ({ id: org.id, label: org.name })),
  )
}

export async function promptAgentSelection(
  client: OmnaraClient,
  orgId: string,
  projectId: string,
): Promise<string> {
  const agents: Choice[] = []
  let cursor: string | undefined
  do {
    const { data } = await sdk.listAgents({
      client,
      path: { orgID: orgId, projectID: projectId },
      query: cursor === undefined ? {} : { cursor },
    })
    agents.push(...data.data.map((agent) => ({ id: agent.id, label: `${agent.name} (${agent.state})` })))
    cursor = data.next_cursor ?? undefined
  } while (cursor !== undefined)
  if (agents.length === 0) {
    throw new CliInputError(`no agents in project ${projectId}`)
  }
  return selectFrom('Select an agent', agents)
}

export async function promptProjectSelection(client: OmnaraClient, orgId: string): Promise<string> {
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
