import { describe, expect, it } from 'vitest'

import { dashboardPageContext } from './page-context'

const home = {
  agent_id: 'agent-home',
  name: 'Assistant',
  can_operate: true,
  scope: {
    org_id: 'org-home',
    project_id: 'project-home',
    org_name: 'Home',
    project_name: 'Default',
  },
}

describe('dashboard page context', () => {
  it('keeps viewed scope separate from agent home and excludes queries and arbitrary route data', () => {
    const params = {
      projectId: 'project-other',
      agentId: 'agent-other',
      accessToken: 'credential',
      search: '?token=secret',
      document: '<private DOM>',
    }
    const context = dashboardPageContext(
      '/projects/project-other/agents/agent-other?token=secret#private',
      params,
      { id: 'org-other', name: 'Other', role: 'owner', created_at: '' },
      home,
    )
    expect(context).toContain(
      JSON.stringify({
        pathname: '/projects/project-other/agents/agent-other',
        viewed_organization: { id: 'org-other', name: 'Other' },
        resources: { projectId: 'project-other', agentId: 'agent-other' },
        assistant_home: {
          organization_id: 'org-home',
          project_id: 'project-home',
          agent_id: 'agent-home',
        },
      }),
    )
    expect(context).not.toMatch(/credential|private|token=|accessToken/)
  })

  it('allows pages before organization creation', () => {
    const context = dashboardPageContext('/onboarding', {}, undefined, home)
    expect(context).toContain('"pathname":"/onboarding"')
    expect(context).not.toContain('viewed_organization')
  })
})
