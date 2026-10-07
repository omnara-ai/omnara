import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import type { WebConfig } from '@omnara/sdk/browser'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterContextProvider,
} from '@tanstack/react-router'
import { act, type ReactNode } from 'react'
import type { Root } from 'react-dom/client'

import type { FakeApi } from './fake-api'

export function renderIntegration(
  root: Root,
  api: FakeApi,
  content: ReactNode,
  webConfig: WebConfig | null = {
    apiURL: 'https://omnara.test/api/v1',
    publicURL: 'https://omnara.test',
  },
) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const cache = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 30_000 } },
  })
  if (webConfig) {
    cache.setQueryDefaults(['web-config'], { enabled: false })
    cache.setQueryData(['web-config'], webConfig)
  }
  const router = createRouter({ routeTree: createRootRoute(), history: createMemoryHistory() })
  function rerender(content: ReactNode) {
    act(() => {
      root.render(
        <OmnaraClientProvider client={client}>
          <QueryClientProvider client={cache}>
            <RouterContextProvider router={router}>{content}</RouterContextProvider>
          </QueryClientProvider>
        </OmnaraClientProvider>,
      )
    })
  }
  rerender(content)
  return { cache, client, rerender }
}
