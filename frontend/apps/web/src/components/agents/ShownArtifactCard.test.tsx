/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { getArtifactOptions } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, StrictMode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest'

import { ShownArtifactCard } from '@/components/agents/ShownArtifactCard'

const mocks = vi.hoisted(() => ({ downloadBlob: vi.fn() }))

vi.mock('@/components/agents/downloadBlob', () => ({ downloadBlob: mocks.downloadBlob }))

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
  mocks.downloadBlob.mockReset()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
  queryClient = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
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

it('renders image previews from the artifact content endpoint', () => {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test' })
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
            <ShownArtifactCard
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
})

it('downloads text artifact content as a Blob', async () => {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test' })
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      artifactMetadata({ contentType: 'text/plain', filename: 'notes.txt', sizeBytes: 13 }),
    )
    .mockResolvedValueOnce(
      new Response('artifact text', {
        headers: { 'Content-Type': 'text/plain' },
        status: 200,
      }),
    )
  client.setConfig({
    fetch,
  })

  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ShownArtifactCard
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

  const download = Array.from(container.querySelectorAll('button')).find(
    (candidate) => candidate.textContent.trim() === 'Download',
  )
  if (!download) throw new Error('Missing download button')

  await act(async () => {
    download.click()
    await vi.waitFor(() => {
      expect(mocks.downloadBlob).toHaveBeenCalledOnce()
    })
  })

  const [content, filename] = mocks.downloadBlob.mock.calls[0] as [Blob, string]
  expect(content).toBeInstanceOf(Blob)
  expect(content.type).toBe('text/plain')
  expect(content.size).toBe(13)
  expect(filename).toBe('notes.txt')
})

it('fetches image content through the SDK only when downloaded', async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      artifactMetadata({ contentType: 'image/png', filename: 'image.png', sizeBytes: 3 }),
    )
    .mockResolvedValueOnce(
      new Response(new Uint8Array([1, 2, 3]), {
        headers: { 'Content-Type': 'image/png' },
        status: 200,
      }),
    )
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test' })
  client.setConfig({ fetch })

  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ShownArtifactCard
            artifactID="art_image"
            orgID="org_test"
            projectID="proj_test"
            agentID="agt_test"
          />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })

  await vi.waitFor(() => {
    expect(container.querySelector('img')).not.toBeNull()
  })
  expect(fetch).toHaveBeenCalledOnce()

  const download = Array.from(container.querySelectorAll('button')).find(
    (candidate) => candidate.textContent.trim() === 'Download',
  )
  if (!download) throw new Error('Missing download button')
  await act(async () => {
    download.click()
    await vi.waitFor(() => {
      expect(mocks.downloadBlob).toHaveBeenCalledOnce()
    })
  })

  expect(fetch).toHaveBeenCalledTimes(2)
})
