import { type OmnaraUIMessage, useOmnaraClient } from '@omnara/react'
import { sdk } from '@omnara/sdk'
import { useRouter } from '@tanstack/react-router'
import { useContext, useEffect, useState } from 'react'

import { dashboardPath, isDashboardPage, type PageRouter } from '@/lib/dashboard-page'

import { AssistantContext } from './assistant-context'
import { createPageActionRunner, pendingPageActions } from './page-actions'
import { findOnPage, pageSnapshot } from './page-snapshot'

const pollMs = 150

async function until<T>(read: () => T | null, timeoutMs: number): Promise<T | null> {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const value = read()
    if (value != null || Date.now() >= deadline) return value
    await new Promise((resolve) => setTimeout(resolve, pollMs))
  }
}

/**
 * Answers the assistant's page tool calls from the user's browser: opening a dashboard
 * page, or pointing at something on the one they are looking at.
 */
export function usePageActions(
  scope: { orgID: string; projectID: string; agentID: string },
  messages: OmnaraUIMessage[],
  enabled: boolean,
) {
  const client = useOmnaraClient()
  // SAFETY: without a RouterProvider (tests, embeds) the hook returns undefined at runtime,
  // which its declared type omits.
  const router = useRouter({ warn: false }) as PageRouter | undefined
  const { point, follow } = useContext(AssistantContext)
  const { orgID, projectID, agentID } = scope
  const [runner] = useState(() =>
    createPageActionRunner({
      visible: () => document.visibilityState === 'visible',
      open: async (path) => {
        const pathname = dashboardPath(path)
        if (router == null || pathname == null || !isDashboardPage(router, path)) return false
        if (pathname !== window.location.pathname) follow()
        router.history.push(path)
        await until(
          () => (window.location.pathname === pathname && !router.state.isLoading ? true : null),
          5000,
        )
        return true
      },
      // The page a just-opened link leads to takes a moment to render.
      find: (target) => until(() => findOnPage(target), 4000),
      point,
      snapshot: pageSnapshot,
      answer: async (toolCallID, outcome, text) => {
        await sdk.submitToolCallResult({
          client,
          path: { orgID, projectID, agentID, toolCallID },
          body: { outcome, content_blocks: [{ type: 'text', text }] },
        })
      },
    }),
  )
  useEffect(() => {
    if (enabled) runner.take(pendingPageActions(messages))
  }, [enabled, messages, runner])
}
