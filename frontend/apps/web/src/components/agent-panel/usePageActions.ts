import { type OmnaraUIMessage, useOmnaraClient } from '@omnara/react'
import { ApiError, sdk } from '@omnara/sdk'
import { useRouter } from '@tanstack/react-router'
import { useContext, useEffect, useState } from 'react'

import { isDashboardPage, type PageRouter } from '@/lib/dashboard-page'

import { AssistantContext } from './assistant-context'
import { clickWithCursor, press, releaseCursor } from './assistant-cursor'
import { answerWhenReady, createPageActionRunner, pageActionsIn } from './page-actions'
import { describeOnPage, findClickableOnPage, findOnPage, pageSnapshot } from './page-snapshot'

const pollMs = 150
// Omnara waits five seconds for the answer to a click. The click and whatever it opens get
// most of that; after it, the answer goes out with whatever is on screen.
const settleMs = 3500
const reactMs = 250
const answerMs = 1200

async function until<T>(read: () => T | null, timeoutMs: number): Promise<T | null> {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const value = read()
    if (value != null || Date.now() >= deadline) return value
    await new Promise((resolve) => setTimeout(resolve, pollMs))
  }
}

function pageSettled(router: PageRouter, pathname: string | null): boolean {
  if (router.state.isLoading) return false
  if (pathname != null && window.location.pathname !== pathname) return false
  const loading = document.querySelectorAll('main [data-slot="skeleton"], main .state-loading')
  return ![...loading].some(
    (element) => element.closest('[data-assistant-surface]') == null && element.checkVisibility(),
  )
}

/**
 * Carries out the assistant's page tool calls as they arrive in the conversation: clicking
 * something and reporting what the screen shows afterwards, or pointing at something.
 */
export function usePageActions(
  scope: { orgID: string; projectID: string; agentID: string },
  messages: OmnaraUIMessage[],
  viewer: { ready: boolean; actorId: string | undefined },
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
      click: async (target) => {
        const element = await until(() => findClickableOnPage(target), 1000)
        if (router == null || element == null) return { outcome: 'missing' }
        const what = describeOnPage(element)
        if (element.matches(':disabled,[aria-disabled="true"]')) {
          return { outcome: 'refused', why: `${what} is disabled, so nothing was clicked.` }
        }
        const link = element instanceof HTMLAnchorElement ? element : null
        const leaves =
          link != null &&
          (link.origin !== window.location.origin || !isDashboardPage(router, link.pathname))
        if (leaves) {
          const why = `${what} leaves the dashboard, so it was not clicked. Give the user the link instead.`
          return { outcome: 'refused', why }
        }
        const from = window.location.pathname
        const deadline = Date.now() + settleMs
        await clickWithCursor(element)
        if (link == null) {
          press(element)
        } else {
          if (link.pathname !== from) follow()
          router.history.push(`${link.pathname}${link.search}${link.hash}`)
        }
        // A menu or dialog takes a moment to appear after the press.
        await new Promise((resolve) => setTimeout(resolve, reactMs))
        const destination = link?.pathname ?? null
        await until(() => (pageSettled(router, destination) ? true : null), deadline - Date.now())
        releaseCursor()
        const now = window.location.pathname
        if (link == null && now !== from) follow()
        return { outcome: 'clicked', what, opened: now === from ? null : now }
      },
      find: (target) => until(() => findOnPage(target), 4000),
      point,
      snapshot: pageSnapshot,
      answer: (toolCallID, outcome, text) =>
        answerWhenReady(
          async () => {
            try {
              await sdk.submitToolCallResult({
                client,
                path: { orgID, projectID, agentID, toolCallID },
                body: { outcome, content_blocks: [{ type: 'text', text }] },
              })
              return 'sent'
            } catch (error) {
              if (error instanceof ApiError && error.code === 'conflict') return 'not_ready'
              throw error
            }
          },
          { timeoutMs: answerMs, pollMs },
        ),
    }),
  )
  const { ready, actorId } = viewer
  useEffect(() => {
    if (ready) runner.take(pageActionsIn(messages, actorId))
  }, [actorId, messages, ready, runner])
  useEffect(() => runner.pause, [runner])
}
