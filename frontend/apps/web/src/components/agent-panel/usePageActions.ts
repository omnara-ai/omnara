import { type UseAgentChatResult, useOmnaraClient } from '@omnara/react'
import { type CompiledTool, createCustomToolWorker } from '@omnara/sdk'
import { useRouter } from '@tanstack/react-router'
import { useContext, useEffect, useEffectEvent } from 'react'

import { isDashboardPage, type PageRouter } from '@/lib/dashboard-page'

import { AssistantContext } from './assistant-context'
import {
  clickWithCursor,
  cursorClickMs,
  pointWithCursor,
  press,
  releaseCursor,
} from './assistant-cursor'
import { createPageTools, type PageActionHost, type PagePointer, startedTurn } from './page-actions'
import {
  describeOnPage,
  matchClickableOnPage,
  matchOnPage,
  type PageMatch,
  pageSnapshot,
} from './page-snapshot'

const pollMs = 150
// How long a target gets to appear before the answer says it is not there.
const findMs = 1000
// A menu or dialog takes a moment to appear after the press.
const reactMs = 250
// The longest a click waits for the page it opened to finish loading.
const settleMs = 3500

async function until<T>(
  read: () => T | null,
  timeoutMs: number,
  signal: AbortSignal,
): Promise<T | null> {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    signal.throwIfAborted()
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

/** What carrying out a page action needs from the dashboard around it. */
interface Surroundings {
  router: PageRouter | undefined
  /** Aborts when the conversation leaves the screen. */
  onScreen: AbortSignal
  startedTurn: (turnID: string) => boolean
  timeoutMs: (tool: string) => number | undefined
  point: (pointer: PagePointer) => void
  follow: () => void
}

function pageActionHost(around: Surroundings): PageActionHost {
  const { router, follow } = around
  return {
    visible: () => document.visibilityState === 'visible',
    startedTurn: around.startedTurn,
    timeoutMs: around.timeoutMs,
    claim: async (id, act) => {
      let done: Awaited<ReturnType<typeof act>> | undefined
      await navigator.locks.request(
        `omnara-assistant-call-${id}`,
        { ifAvailable: true },
        async (lock) => {
          if (lock != null) done = await act()
        },
      )
      return done
    },
    find: async (target, how, { signal, deadline }) => {
      const match = how === 'click' ? matchClickableOnPage : matchOnPage
      const wait = Math.min(findMs, deadline - Date.now() - (how === 'click' ? cursorClickMs : 0))
      const found = await until<PageMatch>(
        () => {
          const current = match(target)
          return current.kind === 'none' ? null : current
        },
        wait,
        signal,
      )
      return found ?? { kind: 'none' }
    },
    click: async (element, { signal, deadline }) => {
      const what = describeOnPage(element)
      if (router == null) return { outcome: 'refused', why: 'Nothing can be clicked here.' }
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
      if (deadline - Date.now() < cursorClickMs + reactMs) {
        const why = `There was no time left to click ${what}, so it was not clicked. Ask for it again.`
        return { outcome: 'refused', why }
      }
      const from = window.location.pathname
      try {
        // Throws if the call is called off, so the press below never follows a stale call.
        await clickWithCursor(element, signal)
        if (link == null) {
          press(element)
        } else {
          if (link.pathname !== from) follow()
          router.history.push(`${link.pathname}${link.search}${link.hash}`)
        }
        await new Promise((resolve) => setTimeout(resolve, reactMs))
        const destination = link?.pathname ?? null
        await until(
          () => (pageSettled(router, destination) ? true : null),
          Math.min(settleMs, deadline - Date.now()),
          signal,
        )
      } finally {
        releaseCursor()
      }
      const opened = window.location.pathname
      if (link == null && opened !== from) follow()
      return { outcome: 'clicked', what, opened: opened === from ? null : opened }
    },
    point: async (pointer) => {
      try {
        await pointWithCursor(pointer.element, around.onScreen)
      } catch {
        return
      }
      around.point(pointer)
      releaseCursor()
    },
    describe: describeOnPage,
    snapshot: pageSnapshot,
  }
}

interface Viewer {
  /** The assistant is the viewer's own. No other agent is given their dashboard. */
  enabled: boolean
  actorId: string | undefined
  /** The assistant's tools as configured, which say how long each waits for an answer. */
  tools: Record<string, CompiledTool> | undefined
}

/**
 * Carries out the assistant's page tool calls in this browser, with its cursor: clicking
 * something and reporting what the screen shows afterwards, or pointing at something and
 * reporting whether it was there. A call is taken up when the live conversation says it
 * became ready, so nothing from before this conversation was on screen ever runs, and
 * everything under way stops when the conversation leaves the screen.
 */
export function usePageActions(
  scope: { orgID: string; projectID: string; agentID: string },
  chat: Pick<UseAgentChatResult, 'events' | 'hasOlderMessages' | 'subscribeToolCallUpdates'>,
  viewer: Viewer,
) {
  const client = useOmnaraClient()
  // SAFETY: without a RouterProvider (tests, embeds) the hook returns undefined at runtime,
  // which its declared type omits.
  const router = useRouter({ warn: false }) as PageRouter | undefined
  const surface = useContext(AssistantContext)
  const { orgID, projectID, agentID } = scope
  const startedTurnNow = useEffectEvent((turnID: string) =>
    startedTurn(chat.events, turnID, viewer.actorId, chat.hasOlderMessages),
  )
  const timeoutNow = useEffectEvent((tool: string) => {
    const seconds = viewer.tools?.[tool]?.timeout_seconds
    return seconds == null ? undefined : seconds * 1000
  })
  const point = useEffectEvent(surface.point)
  const follow = useEffectEvent(surface.follow)

  const { enabled } = viewer
  const { subscribeToolCallUpdates } = chat
  useEffect(() => {
    const onScreen = new AbortController()
    const worker = createCustomToolWorker({
      client,
      path: { orgID, projectID },
      tools: createPageTools(
        pageActionHost({
          router,
          onScreen: onScreen.signal,
          startedTurn: (turnID) => startedTurnNow(turnID),
          timeoutMs: (tool) => timeoutNow(tool),
          point: (pointer) => {
            point(pointer)
          },
          follow: () => {
            follow()
          },
        }),
      ),
    })
    const unsubscribe = subscribeToolCallUpdates((update) => {
      if (!enabled || update.agent_id !== agentID) return
      // A call that is called off, or whose answer cannot be delivered, runs into its timeout.
      void worker.handle(update, { signal: onScreen.signal }).catch(() => undefined)
    })
    return () => {
      unsubscribe()
      onScreen.abort()
    }
  }, [agentID, client, enabled, orgID, projectID, router, subscribeToolCallUpdates])
}
