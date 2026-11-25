import { createOmnaraClient } from '@omnara/sdk'
import { Command } from 'commander'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { registerGroup } from './factory.ts'
import { commandGroups } from './manifest.ts'

const orgID = `org_${'a'.repeat(26)}`
const projectID = `proj_${'a'.repeat(26)}`
const profileID = `aprf_${'a'.repeat(26)}`
const configID = `acfg_${'a'.repeat(26)}`
const apiUrl = 'https://omnara.example/api/v1'
const projectUrl = `${apiUrl}/orgs/${orgID}/projects/${projectID}`
const budgets = [{ interval: 'month', amount_usd: '25' }]
const originalExitCode = process.exitCode

beforeEach(() => {
  vi.spyOn(console, 'log').mockImplementation(() => undefined)
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  process.exitCode = originalExitCode
  vi.restoreAllMocks()
})

async function run(groupName: string, ...args: string[]) {
  const requests: Request[] = []
  const fetch: typeof globalThis.fetch = (input, init) => {
    requests.push(new Request(input, init))
    return Promise.resolve(Response.json({}))
  }
  const group = commandGroups.find((candidate) => candidate.name === groupName)
  if (!group) throw new Error(`${groupName} command is missing`)
  const program = new Command().exitOverride()
  registerGroup(
    program,
    {
      client: createOmnaraClient({ baseUrl: apiUrl, fetch }),
      apiUrl,
      issuerUrl: 'https://omnara.example',
      defaultOrgId: orgID,
      defaultProjectId: projectID,
      store: { path: '', read: () => ({}), update: () => ({}) },
      fetch,
      sleep: () => Promise.resolve(),
      ensureLoggedIn: () => Promise.resolve(),
    },
    group,
  )
  await program.parseAsync([groupName, ...args, '--json'], { from: 'user' })
  return requests
}

describe('initial budgets', () => {
  it.each([
    {
      name: 'agents launch with --budgets',
      group: 'agents',
      args: ['launch', '--profile', profileID, '--budgets', JSON.stringify(budgets)],
      url: `${projectUrl}/agents`,
      body: { profile: profileID, budgets },
    },
    {
      name: 'agents launch with --body',
      group: 'agents',
      args: ['launch', '--body', JSON.stringify({ profile: profileID, budgets })],
      url: `${projectUrl}/agents`,
      body: { profile: profileID, budgets },
    },
    {
      name: 'profiles create with --budgets',
      group: 'profiles',
      args: [
        'create',
        '--name',
        'triage',
        '--config',
        configID,
        '--budgets',
        JSON.stringify(budgets),
      ],
      url: `${projectUrl}/agent-profiles`,
      body: { name: 'triage', config: configID, budgets },
    },
    {
      name: 'profiles create with --body',
      group: 'profiles',
      args: ['create', '--body', JSON.stringify({ name: 'triage', config: configID, budgets })],
      url: `${projectUrl}/agent-profiles`,
      body: { name: 'triage', config: configID, budgets },
    },
  ])('sends budgets from $name', async ({ group, args, url, body }) => {
    const requests = await run(group, ...args)
    expect(requests).toHaveLength(1)
    const [request] = requests
    expect(request?.method).toBe('POST')
    expect(request?.url).toBe(url)
    expect(await request?.json()).toEqual(body)
  })
})
