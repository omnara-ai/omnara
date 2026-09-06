import { describe, expect, it } from 'vitest'

import { chatCommands } from '@/components/overview/onboardingCli'

describe('chatCommands', () => {
  it('uses the deployment API root in SDK and curl examples', () => {
    const commands = chatCommands({
      apiUrl: 'https://api.example.com/v1',
      orgId: 'org_example',
      projectId: 'proj_example',
      profileId: 'apf_example',
      configId: 'apc_example',
    })

    expect(commands.sdk.copy).toContain("baseUrl: 'https://api.example.com/v1'")
    expect(commands.curl.copy).toContain(
      'curl "https://api.example.com/v1/orgs/org_example/projects/proj_example/agents"',
    )
  })
})
