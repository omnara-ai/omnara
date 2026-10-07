/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type Agent, createOmnaraClient, type VisibleProject } from '@omnara/sdk'
import { getAgentProfileOptions } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

import { AgentOrigin } from './AgentOrigin'

const orgId = fakeId('org')
const timestamp = '2026-01-01T00:00:00Z'
const project: VisibleProject = {
  id: fakeId('proj'),
  org_id: orgId,
  name: 'Alpha',
  access: { can_read: true, can_manage: true, can_manage_access: true, can_operate: true },
  created_at: timestamp,
  updated_at: timestamp,
}
const profileId = fakeId('aprf')
const deletedProfileId = `aprf_${'z'.repeat(26)}`

function agent(agentProfileId: string): Agent {
  return {
    id: fakeId('agt'),
    org_id: orgId,
    project_id: project.id,
    agent_profile_id: agentProfileId,
    state: 'active',
    name: 'researcher-1',
    created_at: timestamp,
    updated_at: timestamp,
  }
}

let root: Root
let container: HTMLDivElement
let restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  restore()
})

function origin(id: string) {
  const element = container.querySelector(`[data-case="${id}"]`)
  return {
    text: element?.textContent ?? '',
    links: [...(element?.querySelectorAll('a') ?? [])].map((link) => link.getAttribute('href')),
  }
}

it('links the project when listed across projects and the profile the agent came from', async () => {
  const api = fakeApi([
    {
      method: 'GET',
      path: `/api/v1/orgs/${orgId}/projects/${project.id}/agent-profiles/${deletedProfileId}`,
      respond: () => jsonResponse({ code: 'not_found', message: 'not found' }, 404),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const profileKey: readonly unknown[] = getAgentProfileOptions({
    client,
    path: { orgID: orgId, projectID: project.id, agentProfileID: profileId },
  }).queryKey
  queryClient.setQueryData(profileKey, { id: profileId, name: 'Researcher' })

  const router = createRouter({
    routeTree: createRootRoute({
      component: () => (
        <>
          <p data-case="all-projects">
            <AgentOrigin orgId={orgId} agent={agent(profileId)} project={project} showProfile />
          </p>
          <p data-case="project">
            <AgentOrigin orgId={orgId} agent={agent(profileId)} showProfile />
          </p>
          <p data-case="profile-page">
            <AgentOrigin orgId={orgId} agent={agent(profileId)} showProfile={false} />
          </p>
          <p data-case="profile-page-with-target">
            <AgentOrigin
              orgId={orgId}
              agent={agent(profileId)}
              showProfile={false}
              after={<span>Slack</span>}
            />
          </p>
          <p data-case="project-with-target">
            <AgentOrigin
              orgId={orgId}
              agent={agent(profileId)}
              showProfile
              after={<span>Slack</span>}
            />
          </p>
          <p data-case="deleted-profile">
            <AgentOrigin
              orgId={orgId}
              agent={agent(deletedProfileId)}
              project={project}
              showProfile
            />
          </p>
        </>
      ),
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })

  const profileHref = `/projects/${project.id}/agent-profiles/${profileId}`
  await waitForUI(() => {
    expect(origin('all-projects')).toEqual({
      text: 'AlphaResearcher',
      links: [`/projects/${project.id}`, profileHref],
    })
  })
  expect(origin('project')).toEqual({ text: 'Researcher', links: [profileHref] })
  expect(origin('profile-page')).toEqual({ text: '', links: [] })
  expect(origin('profile-page-with-target').text).toBe('Slack')
  expect(origin('project-with-target').text).toBe('Researcher·Slack')
  await waitForUI(() => {
    expect(
      api.requestsTo(
        'GET',
        `/api/v1/orgs/${orgId}/projects/${project.id}/agent-profiles/${deletedProfileId}`,
      ),
    ).toHaveLength(1)
  })
  expect(origin('deleted-profile')).toEqual({ text: 'Alpha', links: [`/projects/${project.id}`] })
})
