import { describe, expect, it } from 'vitest'

import { inputCommands } from '@/components/overview/onboardingCli'

describe('inputCommands', () => {
  it('uses the deployment API root in SDK and curl examples', () => {
    const commands = inputCommands({
      apiUrl: 'https://api.example.com/v1',
      orgId: 'org_example',
      projectId: 'proj_example',
      agentId: 'agt_example',
    })

    expect(commands.sdk.copy).toContain("baseUrl: 'https://api.example.com/v1'")
    expect(commands.curl.copy).toContain(
      'curl "https://api.example.com/v1/orgs/org_example/projects/proj_example/agents/agt_example/inputs"',
    )
  })
})
