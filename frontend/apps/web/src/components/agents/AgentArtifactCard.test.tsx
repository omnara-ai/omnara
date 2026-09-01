/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { getArtifactOptions } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, StrictMode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { AgentArtifactCard } from '@/components/agents/AgentArtifactCard'

let container: HTMLDivElement
let queryClient: QueryClient
let root: Root
let previousActEnvironment: boolean | undefined

beforeAll(() => {
  const actEnvironment = globalThis as typeof globalThis & {
    IS_REACT_ACT_ENVIRONMENT?: boolean
  }
  previousActEnvironment = actEnvironment.IS_REACT_ACT_ENVIRONMENT
  actEnvironment.IS_REACT_ACT_ENVIRONMENT = true
})

afterAll(() => {
  const actEnvironment = globalThis as typeof globalThis & {
    IS_REACT_ACT_ENVIRONMENT?: boolean
  }
  actEnvironment.IS_REACT_ACT_ENVIRONMENT = previousActEnvironment
})

beforeEach(() => {
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
    },
  })
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  queryClient.clear()
  container.remove()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function artifactMetadata({
  contentType,
  filename,
  sizeBytes,
}: {
  contentType: string
  filename: string
  sizeBytes: number
}) {
  return new Response(
    JSON.stringify({
      id: 'art_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      org_id: 'org_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      project_id: 'proj_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      content_type: contentType,
      filename,
      size_bytes: sizeBytes,
      created_at: '2026-08-27T00:00:00Z',
    }),
    { headers: { 'Content-Type': 'application/json' }, status: 200 },
  )
}

it('renders artifact images directly from the content endpoint', () => {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  const path = {
    orgID: 'org_test',
    projectID: 'proj_test',
    agentID: 'agt_test',
    artifactID: 'art_image',
  }
  queryClient.setQueryData(getArtifactOptions({ client, path }).queryKey, {
    id: 'art_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    org_id: 'org_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    project_id: 'proj_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    agent_id: 'agt_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    content_type: 'image/png',
    filename: 'image.png',
    size_bytes: 3,
    created_at: '2026-08-27T00:00:00Z',
  })

  act(() => {
    root.render(
      <StrictMode>
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={queryClient}>
            <AgentArtifactCard
              artifactID={path.artifactID}
              orgID={path.orgID}
              projectID={path.projectID}
              agentID={path.agentID}
            />
          </QueryClientProvider>
        </OmnaraClientProvider>
      </StrictMode>,
    )
  })

  const image = container.querySelector('img')
  expect(image?.src).toBe(
    'https://omnara.test/api/v1/orgs/org_test/projects/proj_test/agents/agt_test/artifacts/art_image/content',
  )
  expect(image?.loading).toBe('lazy')
  expect(container.querySelector('a')?.href).toBe(image?.src)
})

it('renders optimistic attachments with the same card', () => {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })

  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <AgentArtifactCard
            orgID="org_test"
            projectID="proj_test"
            agentID="agt_test"
            data="AQID"
            mediaType="image/png"
            filename="image.png"
            sizeBytes={3}
          />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })

  expect(container.querySelector('img')?.src).toBe('data:image/png;base64,AQID')
  expect(container.textContent).toContain('image.png')
  expect(container.textContent).toContain('3 B')
  expect(container.querySelector('a')?.download).toBe('image.png')
})

it('loads artifact metadata for durable attachments', async () => {
  const fetch = vi
    .fn()
    .mockResolvedValue(
      artifactMetadata({ contentType: 'text/plain', filename: 'notes.txt', sizeBytes: 13 }),
    )
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch })

  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <AgentArtifactCard
            artifactID="art_test"
            orgID="org_test"
            projectID="proj_test"
            agentID="agt_test"
          />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })

  await vi.waitFor(() => {
    expect(container.textContent).toContain('notes.txt')
  })
  expect(fetch).toHaveBeenCalledOnce()
  expect(container.querySelector('a')?.href).toBe(
    'https://omnara.test/api/v1/orgs/org_test/projects/proj_test/agents/agt_test/artifacts/art_test/content',
  )
})
