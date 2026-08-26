/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
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

it('renders image previews from the artifact content endpoint', () => {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test' })

  act(() => {
    root.render(
      <StrictMode>
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={queryClient}>
            <ShownArtifactCard
              artifact={{
                artifactId: 'art_image',
                contentType: 'image/png',
                filename: 'image.png',
                sizeBytes: 3,
              }}
              orgID="org_test"
              projectID="proj_test"
              agentID="agt_test"
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
  client.setConfig({
    fetch: vi.fn(() =>
      Promise.resolve(
        new Response('artifact text', {
          headers: { 'Content-Type': 'text/plain' },
          status: 200,
        }),
      ),
    ),
  })

  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ShownArtifactCard
            artifact={{
              artifactId: 'art_test',
              contentType: 'text/plain',
              filename: 'notes.txt',
              sizeBytes: 13,
            }}
            orgID="org_test"
            projectID="proj_test"
            agentID="agt_test"
          />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
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
  const fetch = vi.fn(() =>
    Promise.resolve(
      new Response(new Uint8Array([1, 2, 3]), {
        headers: { 'Content-Type': 'image/png' },
        status: 200,
      }),
    ),
  )
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test' })
  client.setConfig({ fetch })

  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <ShownArtifactCard
            artifact={{
              artifactId: 'art_image',
              contentType: 'image/png',
              filename: 'image.png',
              sizeBytes: 3,
            }}
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
  expect(fetch).not.toHaveBeenCalled()

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

  expect(fetch).toHaveBeenCalledOnce()
})
