// Tears down aborted runs: archives every agent under the given run profiles
// (which releases their machines) and deletes the profiles.
//
//   pnpm tb:cleanup aprf_... aprf_...
import { sdk } from '@omnara/sdk'

import { connect } from './omnara.ts'

const profileIDs = process.argv.slice(2)
if (profileIDs.length === 0) {
  console.error('usage: pnpm tb:cleanup <agent-profile-id>...')
  process.exit(1)
}

const omnara = connect({})
const path = { orgID: omnara.orgID, projectID: omnara.projectID }

for (const profileID of profileIDs) {
  const agents: string[] = []
  let cursor: string | undefined
  do {
    const page = await sdk.listAgents({
      client: omnara.client,
      path,
      query: { agent_profile_id: profileID, limit: 100, cursor },
    })
    agents.push(...page.data.data.map((agent) => agent.id))
    cursor = page.data.next_cursor ?? undefined
  } while (cursor !== undefined)

  await Promise.all(
    agents.map((agentID) => sdk.archiveAgent({ client: omnara.client, path: { ...path, agentID } })),
  )
  await sdk.deleteAgentProfile({ client: omnara.client, path: { ...path, agentProfileID: profileID } })
  console.log(`${profileID}: archived ${agents.length} agent(s), deleted profile`)
}
