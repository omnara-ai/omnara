import { type AgentChatScope, OmnaraClientProvider } from '@omnara/react'
import type { OmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from 'ink'

import { Chat } from './chat-ui.tsx'

export async function runChat(client: OmnaraClient, scope: AgentChatScope): Promise<void> {
  const queryClient = new QueryClient()
  const app = render(
    <QueryClientProvider client={queryClient}>
      <OmnaraClientProvider client={client}>
        <Chat scope={scope} />
      </OmnaraClientProvider>
    </QueryClientProvider>,
  )
  try {
    await app.waitUntilExit()
  } finally {
    queryClient.clear()
  }
}
