import type {
  AgentEvent,
  CustomToolCall,
  CustomToolHandler,
  CustomToolHandlerResult,
  SubmitToolCallResultRequest,
} from '@omnara/sdk'
import * as z from 'zod'

import type { PageMatch } from './page-snapshot'

// The assistant's two page tools, both carried out with its own cursor in the browser of the
// person who asked. Clicking is how it acts on the screen and the only way it moves the
// user; the answer is what the screen shows afterwards. Pointing marks something; the answer
// says whether it was found. A call is carried out only once Omnara says it is ready, and
// only within the time its tool waits for an answer.
export const clickTool = 'dashboard_click'
export const pointAtTool = 'dashboard_point_at'

const clickInput = z.object({ target: z.string().min(1) })
const pointInput = z.object({ target: z.string().min(1), note: z.string().optional() })

// What a call keeps back from its tool's timeout for the answer to reach Omnara.
const answerMs = 500

/** What a click came to. `what` words the element the way the page outline does. */
export type PageClick =
  { outcome: 'clicked'; what: string; opened: string | null } | { outcome: 'refused'; why: string }

export interface PagePointer {
  element: Element
  note: string | undefined
}

/** When a call has to stop: Omnara gave up on it or the conversation left the screen, or its time ran out. */
export interface PageActionLimits {
  signal: AbortSignal
  /** Epoch milliseconds by which the answer has to be on its way. */
  deadline: number
}

/**
 * Whether this person, and nobody else, gave the assistant everything it was told in a turn.
 * A turn's events sit together in the conversation, so all of its inputs are on hand once
 * something older than the turn is loaded or nothing older exists; until then the answer is no.
 */
export function startedTurn(
  events: AgentEvent[],
  turnID: string,
  actorID: string | undefined,
  olderEventsExist: boolean,
): boolean {
  if (actorID == null) return false
  const first = events.findIndex((event) => event.turn_id === turnID)
  if (first === -1 || (first === 0 && olderEventsExist)) return false
  const inputs = events.flatMap((event) =>
    event.turn_id === turnID && event.event_kind === 'agent_input' ? [event] : [],
  )
  return inputs.length > 0 && inputs.every((input) => input.actor_id === actorID)
}

export interface PageActionHost {
  /** Whether the user is looking at this tab. */
  visible: () => boolean
  /** Whether the person at this dashboard, alone, started the turn. */
  startedTurn: (turnID: string) => boolean
  /** How long the tool waits for an answer, if it stops waiting at all. */
  timeoutMs: (tool: string) => number | undefined
  /**
   * Carries out a call unless another window of this browser already has it: with the
   * dashboard open in two windows, a click must still happen once.
   */
  claim: <T>(id: string, act: () => Promise<T>) => Promise<T | undefined>
  /** What the target is on the page, giving it a moment to appear. */
  find: (target: string, how: 'click' | 'point', limits: PageActionLimits) => Promise<PageMatch>
  click: (element: Element, limits: PageActionLimits) => Promise<PageClick>
  /** Takes the cursor to the element and marks it. */
  point: (pointer: PagePointer) => Promise<void>
  /** The element as the page outline words it. */
  describe: (element: Element) => string
  /** The page as the assistant reads it. */
  snapshot: () => string
}

function answer(outcome: 'succeeded' | 'failed', text: string): SubmitToolCallResultRequest {
  return { outcome, content_blocks: [{ type: 'text', text }] }
}

function unresolved(match: Exclude<PageMatch, { kind: 'one' }>, target: string): string {
  const named = JSON.stringify(target)
  if (match.kind === 'several') {
    return `${String(match.refs.length)} different things on the user's screen match ${named}: ${match.refs.join(', ')}. Give the ref of the one you mean; the outline below shows where each one is.`
  }
  if (match.kind === 'gone') return `${named} is no longer on the user's screen.`
  return `Nothing matching ${named} is on the user's screen.`
}

/** What a call comes to, and what it still has the cursor doing after its answer is known. */
interface Carried {
  result: SubmitToolCallResultRequest
  rest?: Promise<void>
}

/**
 * The handlers that carry out the page tools for a custom tool worker. A call that is not
 * this dashboard's to answer is left alone, for whoever started its turn or for its timeout.
 */
export function createPageTools(host: PageActionHost) {
  const seen = () => `What the user sees now:\n${host.snapshot()}`

  // There is one cursor, so calls are carried out one at a time in the order they arrived.
  let cursor: Promise<unknown> = Promise.resolve()
  function inOrder(act: () => Promise<Carried>): Promise<SubmitToolCallResultRequest> {
    const carried = cursor.then(act)
    cursor = carried.then(({ rest }) => rest).catch(() => undefined)
    return carried.then(({ result }) => result)
  }

  async function click(call: CustomToolCall, limits: PageActionLimits): Promise<Carried> {
    const input = clickInput.safeParse(call.input)
    if (!input.success) return { result: answer('failed', 'Say what to click.') }
    const { target } = input.data
    const match = await host.find(target, 'click', limits)
    if (match.kind !== 'one') {
      return { result: answer('failed', `${unresolved(match, target)}\n\n${seen()}`) }
    }
    const done = await host.click(match.element, limits)
    if (done.outcome === 'refused') {
      return { result: answer('failed', `${done.why}\n\n${seen()}`) }
    }
    const opened = done.opened == null ? '' : ` It opened ${done.opened}.`
    return { result: answer('succeeded', `Clicked ${done.what}.${opened}\n\n${seen()}`) }
  }

  async function pointAt(call: CustomToolCall, limits: PageActionLimits): Promise<Carried> {
    const input = pointInput.safeParse(call.input)
    if (!input.success) return { result: answer('failed', 'Say what to point at.') }
    const { target, note } = input.data
    const match = await host.find(target, 'point', limits)
    if (match.kind !== 'one') {
      return { result: answer('failed', `${unresolved(match, target)}\n\n${seen()}`) }
    }
    // Answered while the cursor travels, so neither the assistant nor the user waits.
    const what = host.describe(match.element)
    return {
      result: answer('succeeded', `Pointed at ${what}. The user can see it marked.`),
      rest: host.point({ element: match.element, note }),
    }
  }

  const tool =
    (
      name: string,
      carryOut: (call: CustomToolCall, limits: PageActionLimits) => Promise<Carried>,
    ): CustomToolHandler =>
    async (call): Promise<CustomToolHandlerResult> => {
      if (!host.visible() || !host.startedTurn(call.turnID)) return { outcome: 'deferred' }
      const timeoutMs = host.timeoutMs(name)
      const limits = {
        signal: call.signal,
        deadline: timeoutMs == null ? Infinity : Date.now() + timeoutMs - answerMs,
      }
      const result = await host.claim(call.toolCallID, () =>
        inOrder(() => {
          // Waiting for an earlier call may have outlasted this one.
          call.signal.throwIfAborted()
          return carryOut(call, limits)
        }),
      )
      return result ?? { outcome: 'deferred' }
    }

  return {
    [clickTool]: tool(clickTool, click),
    [pointAtTool]: tool(pointAtTool, pointAt),
  } satisfies Record<string, CustomToolHandler>
}
