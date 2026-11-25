import type { OmnaraUIMessage } from '@omnara/react'
import * as z from 'zod'

// The assistant's two page tools, both carried out with its own cursor and both answered
// from the user's browser. Clicking is how it acts on the screen and the only way it moves
// the user; the answer is what the screen shows afterwards. Pointing marks something; the
// answer says whether it was found. Omnara stops waiting after a few seconds when no
// dashboard answers.
export const clickTool = 'dashboard_click'
export const pointAtTool = 'dashboard_point_at'

const clickInput = z.object({ target: z.string().min(1) })
const pointInput = z.object({ target: z.string().min(1), note: z.string().optional() })

export type PageAction =
  | { id: string; tool: typeof clickTool; target: string; waiting: boolean }
  | {
      id: string
      tool: typeof pointAtTool
      target: string
      note: string | undefined
      waiting: boolean
    }

/** What a click came to. `what` words the element the way the page outline does. */
export type PageClick =
  | { outcome: 'clicked'; what: string; opened: string | null }
  | { outcome: 'refused'; why: string }
  | { outcome: 'missing' }

export interface PagePointer {
  element: Element
  note: string | undefined
}

/**
 * The page tool calls in a conversation, oldest first, from the turns this person started.
 * A turn someone or something else started never moves this person's screen.
 */
export function pageActionsIn(
  messages: OmnaraUIMessage[],
  actorId: string | undefined,
): PageAction[] {
  const actions: PageAction[] = []
  let mine = false
  for (const message of messages) {
    if (message.role === 'user') {
      mine = actorId != null && message.metadata?.actorId === actorId
      continue
    }
    if (!mine) continue
    for (const part of message.parts) {
      if (part.type !== 'dynamic-tool' || part.toolType !== 'custom') continue
      if (part.state === 'input-streaming') continue
      const waiting = part.state === 'input-available'
      if (part.toolName === clickTool) {
        const input = clickInput.safeParse(part.input)
        if (input.success) {
          actions.push({ id: part.toolCallId, tool: clickTool, target: input.data.target, waiting })
        }
      }
      if (part.toolName === pointAtTool) {
        const input = pointInput.safeParse(part.input)
        if (input.success) {
          const { target, note } = input.data
          actions.push({ id: part.toolCallId, tool: pointAtTool, target, note, waiting })
        }
      }
    }
  }
  return actions
}

/**
 * Sends an answer, trying again while the call cannot take one yet: a call shows up in the
 * conversation a moment before Omnara is ready for its result.
 */
export async function answerWhenReady(
  send: () => Promise<'sent' | 'not_ready'>,
  options: { timeoutMs: number; pollMs: number },
): Promise<void> {
  const deadline = Date.now() + options.timeoutMs
  while ((await send()) === 'not_ready' && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, options.pollMs))
  }
}

export interface PageActionHost {
  /** Whether the user is looking at this tab. */
  visible: () => boolean
  /**
   * Carries out a call unless another window of this browser already has it: with the
   * dashboard open in two windows, a click must still happen once.
   */
  claim: (id: string, act: () => Promise<void>) => Promise<void>
  click: (target: string) => Promise<PageClick>
  find: (target: string) => Promise<Element | null>
  /** The element as the page outline words it. */
  describe: (element: Element) => string
  /** Takes the cursor to the element and marks it. */
  point: (pointer: PagePointer) => Promise<void>
  /** The page as the assistant reads it. */
  snapshot: () => string
  answer: (id: string, outcome: 'succeeded' | 'failed', text: string) => Promise<void>
}

export function createPageActionRunner(host: PageActionHost) {
  const seen = new Set<string>()
  let watching = false
  let queue = Promise.resolve()

  async function run(action: PageAction) {
    // Omnara has already stopped waiting for this one, so nothing happens late.
    if (!action.waiting) return
    const seen = () => `What the user sees now:\n${host.snapshot()}`
    if (action.tool === pointAtTool) {
      const element = await host.find(action.target)
      if (element == null) {
        const why = `Nothing matching ${JSON.stringify(action.target)} is on the user's screen.`
        await host.answer(action.id, 'failed', `${why}\n\n${seen()}`)
        return
      }
      // Answered while the cursor travels, so neither the assistant nor the user waits.
      const what = host.describe(element)
      await Promise.all([
        host.answer(action.id, 'succeeded', `Pointed at ${what}. The user can see it marked.`),
        host.point({ element, note: action.note }),
      ])
      return
    }
    const click = await host.click(action.target)
    if (click.outcome === 'clicked') {
      const opened = click.opened == null ? '' : ` It opened ${click.opened}.`
      await host.answer(action.id, 'succeeded', `Clicked ${click.what}.${opened}\n\n${seen()}`)
      return
    }
    const why =
      click.outcome === 'refused'
        ? click.why
        : `Nothing to click matching ${JSON.stringify(action.target)} is on the user's screen.`
    await host.answer(action.id, 'failed', `${why}\n\n${seen()}`)
  }

  return {
    /**
     * Takes the conversation as it is now. What is already there when watching starts is
     * history; only calls that arrive afterwards run, once each, in order.
     */
    take: (actions: PageAction[]) => {
      const fresh = actions.filter((action) => !seen.has(action.id))
      for (const action of fresh) seen.add(action.id)
      if (!watching) {
        watching = true
        return
      }
      if (!host.visible()) return
      for (const action of fresh) {
        queue = queue.then(() => host.claim(action.id, () => run(action))).catch(() => undefined)
      }
    },
    /** Stops watching, so a call that arrives while the conversation is hidden never runs late. */
    pause: () => {
      watching = false
    },
    settled: () => queue,
  }
}
