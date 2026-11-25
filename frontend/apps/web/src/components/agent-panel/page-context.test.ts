import { describe, expect, it, vi } from 'vitest'

import { dashboardPageContext } from './page-context'

const home = {
  agent_id: 'agent-home',
  name: 'Assistant',
  can_operate: true,
  own_assistant: true,
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
    const context = dashboardPageContext({
      pathname: '/projects/project-other/agents/agent-other?token=secret#private',
      route: '/projects/$projectId/agents/$agentId',
      params,
      snapshot: () => '# Reviewer\nbutton "Send message"',
      docked: false,
      viewedOrg: { id: 'org-other', name: 'Other', role: 'owner', created_at: '' },
      selection: home,
    })
    expect(context).toContain(
      JSON.stringify({
        surface: 'assistant panel, floating over the page from the bottom-right corner',
        pathname: '/projects/project-other/agents/agent-other',
        route: '/projects/$projectId/agents/$agentId',
        viewed_organization: { id: 'org-other', name: 'Other' },
        resources: { projectId: 'project-other', agentId: 'agent-other' },
        assistant_home: {
          organization_id: 'org-home',
          project_id: 'project-home',
          agent_id: 'agent-home',
        },
      }),
    )
    expect(context).toContain('What the user sees:\n# Reviewer\nbutton "Send message"')
    expect(context).not.toMatch(/credential|private|token=|accessToken/)
  })

  it('allows pages before organization creation', () => {
    const context = dashboardPageContext({
      pathname: '/onboarding',
      route: '/onboarding',
      params: {},
      snapshot: () => '',
      docked: true,
      viewedOrg: undefined,
      selection: home,
    })
    expect(context).toContain('"pathname":"/onboarding"')
    expect(context).toContain('"surface":"the conversation is the main column of the page"')
    expect(context).not.toContain('viewed_organization')
  })

  it('tells an agent that is not the viewer’s own assistant nothing, without reading the page', () => {
    const snapshot = vi.fn(() => 'button "Invite user"')
    const context = dashboardPageContext({
      pathname: '/members',
      route: '/members',
      params: {},
      snapshot,
      docked: false,
      viewedOrg: { id: 'org-other', name: 'Other', role: 'owner', created_at: '' },
      selection: { ...home, own_assistant: false },
    })
    expect(context).toBeUndefined()
    expect(snapshot).toHaveBeenCalledTimes(0)
  })
})
