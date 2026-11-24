import type { OmnaraUIMessage } from '@omnara/react'
import * as z from 'zod'

// The assistant's page tools are custom tools with a short timeout. The panel answers each
// call from the user's browser; with no dashboard open Omnara answers "timed out" instead,
// so the assistant never waits on a page nobody is looking at.
export const openPageTool = 'dashboard_open_page'
export const pointAtTool = 'dashboard_point_at'

const openInput = z.object({ path: z.string().min(1) })
const pointInput = z.object({ target: z.string().min(1), note: z.string().optional() })

export type PageAction =
  | { id: string; tool: typeof openPageTool; path: string }
  | { id: string; tool: typeof pointAtTool; target: string; note: string | undefined }

export interface PagePointer {
  element: Element
  note: string | undefined
}

/** The page tool calls in a conversation that are still waiting for an answer, oldest first. */
export function pendingPageActions(messages: OmnaraUIMessage[]): PageAction[] {
  const actions: PageAction[] = []
  for (const part of messages.flatMap((message) => message.parts)) {
    if (part.type !== 'dynamic-tool' || part.toolType !== 'custom') continue
    if (part.state !== 'input-available') continue
    if (part.toolName === openPageTool) {
      const input = openInput.safeParse(part.input)
      if (input.success) {
        actions.push({ id: part.toolCallId, tool: openPageTool, path: input.data.path })
      }
    }
    if (part.toolName === pointAtTool) {
      const input = pointInput.safeParse(part.input)
      if (input.success) {
        const { target, note } = input.data
        actions.push({ id: part.toolCallId, tool: pointAtTool, target, note })
      }
    }
  }
  return actions
}

export interface PageActionHost {
  /** Whether the user is looking at this tab. */
  visible: () => boolean
  /** Opens the page; false when the path is not a dashboard page. */
  open: (path: string) => Promise<boolean>
  find: (target: string) => Promise<Element | null>
  point: (pointer: PagePointer) => void
  /** The page as the assistant reads it. */
  snapshot: () => string
  answer: (id: string, outcome: 'succeeded' | 'failed', text: string) => Promise<void>
}

export function createPageActionRunner(host: PageActionHost) {
  const taken = new Set<string>()
  let queue = Promise.resolve()

  const seen = () => `What the user sees now:\n${host.snapshot()}`

  async function run(action: PageAction) {
    if (action.tool === openPageTool) {
      const opened = await host.open(action.path)
      await (opened
        ? host.answer(action.id, 'succeeded', `Opened ${action.path}.\n\n${seen()}`)
        : host.answer(action.id, 'failed', `${action.path} is not a dashboard page.`))
      return
    }
    const element = await host.find(action.target)
    if (element == null) {
      await host.answer(
        action.id,
        'failed',
        `Nothing on the page is named "${action.target}".\n\n${seen()}`,
      )
      return
    }
    host.point({ element, note: action.note })
    await host.answer(action.id, 'succeeded', 'The user can see it marked on the page.')
  }

  return {
    /** Answers each waiting call once, in order. A tab nobody is looking at leaves them be. */
    observe: (actions: PageAction[]) => {
      if (!host.visible()) return
      for (const action of actions) {
        if (taken.has(action.id)) continue
        taken.add(action.id)
        queue = queue.then(() => run(action)).catch(() => undefined)
      }
    },
    settled: () => queue,
  }
}
