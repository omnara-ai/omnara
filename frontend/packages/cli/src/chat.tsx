import { OmnaraClientProvider } from '@omnara/react'
import type { OmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from 'ink'

import { Chat } from './chat-ui.tsx'

export interface ChatTarget {
  client: OmnaraClient
  orgId: string
  projectId: string
  agentId: string
}

export async function runChat({ client, orgId, projectId, agentId }: ChatTarget): Promise<void> {
  const queryClient = new QueryClient()
  const app = render(
    <QueryClientProvider client={queryClient}>
      <OmnaraClientProvider client={client}>
        <Chat scope={{ orgID: orgId, projectID: projectId, agentID: agentId }} />
      </OmnaraClientProvider>
    </QueryClientProvider>,
  )
  try {
    await app.waitUntilExit()
  } finally {
    queryClient.clear()
  }
}
