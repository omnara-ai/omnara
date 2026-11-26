import type {
  AgentEvent,
  CustomToolCall,
  CustomToolCallRef,
  CustomToolHandler,
  CustomToolHandlerResult,
  SubmitToolCallResultRequest,
} from '@omnara/sdk'
import * as z from 'zod'

import type { PageMatch } from './page-snapshot'

// The assistant's two page tools, both carried out with its own cursor in the browser of the
// person who asked. Clicking is how it acts on the screen and the only way it moves the
// user; the answer is what the screen shows afterwards. Pointing marks something; the answer
// says whether it was found. A call is carried out only once Omnara says it is ready, only
// in the window whose message the assistant was answering, and only within the time the call
// itself has left.
export const clickTool = 'dashboard_click'
export const pointAtTool = 'dashboard_point_at'

const clickInput = z.object({ target: z.string().min(1) })
const pointInput = z.object({ target: z.string().min(1), note: z.string().optional() })

// What a call keeps back from the time it has left for the answer to reach Omnara.
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
  /**
   * Asks Omnara whether the call still wants an answer and has time for one. Asked at the
   * last moment before something that cannot be taken back.
   */
  stillWaiting: () => Promise<boolean>
}

/** Who is at this dashboard: the actor they act as, and the messages this window sent. */
export interface Asker {
  actorID: string | undefined
  sent: (idempotencyKey: string | undefined) => boolean
}

/**
 * Whether a call is this window's to carry out. This person, and nobody else, gave the
 * assistant everything it was told in the call's turn, and the message it was answering when
 * it made the call was sent from this window. The outline that went with that message, and
 * so every ref the call can name, belongs to this window and to no other.
 *
 * A turn's events sit together in the conversation, so all of its inputs are on hand once
 * something older than the turn is loaded or nothing older exists; until then the answer is no.
 */
export function askedFromHere(
  events: AgentEvent[],
  call: { turnID: string; toolCallID: string },
  asker: Asker,
  olderEventsExist: boolean,
): boolean {
  if (asker.actorID == null) return false
  const first = events.findIndex((event) => event.turn_id === call.turnID)
  if (first === -1 || (first === 0 && olderEventsExist)) return false
  const turn = events.filter((event) => event.turn_id === call.turnID)
  const inputs = turn.flatMap((event) => (event.event_kind === 'agent_input' ? [event] : []))
  if (inputs.length === 0 || inputs.some((input) => input.actor_id !== asker.actorID)) return false
  const made = turn.find(
    (event) =>
      event.event_kind === 'model_output' &&
      event.content_blocks.some(
        (block) => block.type === 'tool_call' && block.tool_call_id === call.toolCallID,
      ),
  )
  if (made == null) return false
  const answering = inputs
    .filter((input) => input.input_kind === 'content' && input.sequence < made.sequence)
    .at(-1)
  return answering != null && asker.sent(answering.input_idempotency_key)
}

export interface PageActionHost {
  /** Whether the user is looking at this tab. */
  visible: () => boolean
  /** Whether the call is this window's to carry out; see `askedFromHere`. */
  ours: (call: { turnID: string; toolCallID: string }) => boolean
  /** The call as Omnara has it now: whether it is still ready, and the time it has left. */
  read: (call: CustomToolCallRef) => Promise<{ ready: boolean; leftMs: number | undefined }>
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
      carryOut: (call: CustomToolCall, limits: PageActionLimits) => Promise<Carried>,
    ): CustomToolHandler =>
    (call): Promise<CustomToolHandlerResult> => {
      if (!host.visible() || !host.ours(call)) return Promise.resolve({ outcome: 'deferred' })
      const limits: PageActionLimits = {
        signal: call.signal,
        deadline: call.answerBy == null ? Infinity : call.answerBy - answerMs,
        stillWaiting: async () => {
          const now = await host.read(call).catch(() => null)
          return now != null && now.ready && (now.leftMs == null || now.leftMs > answerMs)
        },
      }
      return inOrder(() => {
        // Waiting for an earlier call may have outlasted this one.
        call.signal.throwIfAborted()
        return carryOut(call, limits)
      })
    }

  return {
    [clickTool]: tool(click),
    [pointAtTool]: tool(pointAt),
  } satisfies Record<string, CustomToolHandler>
}
