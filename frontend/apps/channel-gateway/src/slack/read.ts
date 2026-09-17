import {
  type OperationAttemptContext,
  type OperationRetryOptions,
  retryOperation,
} from '../operations/retry'
import { type SlackClient } from './client'
import { SlackAPIError } from './errors'
import type { SlackHistoryMessage } from './messages'
import { type SlackHistory, slackHistoryCursor, type SlackMessage } from './protocol'
import { type SlackDestination, validateDestination } from './send'

const historyBytes = 1024 * 1024 // Same bound as one provider/operation response.

export interface SlackReadInput extends SlackDestination {
  limit: number
  /** Provider cursor only. Core must bind its public cursor to agent/channel authority. */
  cursor?: string
}

export interface SlackHistoryPage {
  messages: SlackHistoryMessage[]
  nextCursor?: string
  coverage: 'complete' | 'partial'
}

export async function readSlackHistory(
  client: SlackClient,
  parseMessage: (raw: SlackMessage) => SlackHistoryMessage,
  input: SlackReadInput,
  operation: Omit<OperationRetryOptions, 'idempotent'>,
): Promise<SlackHistoryPage> {
  validateDestination(input)
  if (!Number.isInteger(input.limit) || input.limit < 1 || input.limit > 100)
    throw new SlackAPIError('invalid_history_limit')
  const before = decodeCursor(input)
  return retryOperation({ ...operation, idempotent: true }, async (context) => {
    const page = input.threadTs
      ? await recentThreadMessages(client, input, before, context)
      : await channelMessages(client, input, before, context)
    const messages = page.messages.map(parseMessage)
    if (Buffer.byteLength(JSON.stringify(messages)) > historyBytes)
      throw new SlackAPIError('history_too_large')
    const oldest = messages[0]?.timestamp
    return {
      messages,
      coverage:
        page.limited || messages.some((message) => message.partial) ? 'partial' : 'complete',
      nextCursor: page.more && oldest ? encodeCursor(input, oldest) : undefined,
    }
  })
}

async function channelMessages(
  client: SlackClient,
  input: SlackReadInput,
  before: string | undefined,
  context: OperationAttemptContext,
) {
  const response = await client.api(
    'conversations.history',
    {
      channel: input.channel,
      limit: input.limit,
      inclusive: false,
      latest: before,
    },
    context,
  )
  const messages = historyMessages(response, input, before)
  if (messages.length > input.limit) throw new SlackAPIError('invalid_history_response')
  messages.sort((a, b) => compareTimestamp(a.ts, b.ts))
  const more = hasMore(response)
  if (more && messages.length === 0) throw new SlackAPIError('nonadvancing_history')
  // Slack reports retained-but-inaccessible free-plan history independently of pagination.
  return { messages, more, limited: response.is_limited === true }
}

async function recentThreadMessages(
  client: SlackClient,
  input: SlackReadInput,
  before: string | undefined,
  context: OperationAttemptContext,
) {
  // replies is oldest-first. Scan to the end under the one deadline, retaining
  // only limit+1 entries. Taking the tail of just its first page loses new replies.
  if (!input.threadTs) throw new SlackAPIError('invalid_destination')
  let tail: SlackMessage[] = []
  let cursor: string | undefined
  let previous: string | undefined
  let retainedBytes = 0
  let limited = false
  const sizes: number[] = []
  for (;;) {
    const response = await client.api(
      'conversations.replies',
      {
        channel: input.channel,
        ts: input.threadTs,
        limit: 100,
        inclusive: false,
        latest: before,
        cursor,
      },
      context,
    )
    const page = historyMessages(response, input, before)
    limited ||= response.is_limited === true
    for (const message of page) {
      if (previous && compareTimestamp(message.ts, previous) <= 0)
        throw new SlackAPIError('nonadvancing_history')
      previous = message.ts
      tail.push(message)
      const size = Buffer.byteLength(JSON.stringify(message))
      sizes.push(size)
      retainedBytes += size
      if (tail.length > input.limit + 1) {
        tail.shift()
        retainedBytes -= sizes.shift() ?? 0
      }
      if (retainedBytes > historyBytes) throw new SlackAPIError('history_too_large')
    }
    if (!hasMore(response)) break
    const next = providerCursor(response)
    if (!next || next === cursor || page.length === 0)
      throw new SlackAPIError('nonadvancing_history')
    cursor = next
  }
  const more = tail.length > input.limit
  tail = tail.slice(-input.limit)
  return { messages: tail, more, limited }
}

function historyMessages(response: SlackHistory, input: SlackReadInput, before?: string) {
  return response.messages.map((value) => {
    if (
      (value.channel !== undefined && value.channel !== input.channel) ||
      (input.threadTs && value.thread_ts !== undefined && value.thread_ts !== input.threadTs) ||
      (before && compareTimestamp(value.ts, before) >= 0)
    )
      throw new SlackAPIError('invalid_history_response')
    return { ...value, ts: value.ts, channel: input.channel }
  })
}

function hasMore(response: SlackHistory): boolean {
  return response.has_more === true || providerCursor(response) !== undefined
}

function providerCursor(response: SlackHistory): string | undefined {
  const cursor = response.response_metadata?.next_cursor
  return cursor === '' ? undefined : cursor
}

function encodeCursor(input: SlackDestination, before: string): string {
  return Buffer.from(
    JSON.stringify({ channel: input.channel, threadTs: input.threadTs ?? null, before }),
  ).toString('base64url')
}

function decodeCursor(input: SlackReadInput): string | undefined {
  if (input.cursor === undefined) return undefined
  if (!input.cursor || input.cursor.length > 4096 || !/^[\w-]+$/.test(input.cursor))
    throw new SlackAPIError('invalid_history_cursor')
  let value: unknown
  try {
    value = JSON.parse(Buffer.from(input.cursor, 'base64url').toString('utf8'))
  } catch {
    throw new SlackAPIError('invalid_history_cursor')
  }
  const parsed = slackHistoryCursor.safeParse(value)
  if (
    !parsed.success ||
    parsed.data.channel !== input.channel ||
    parsed.data.threadTs !== (input.threadTs ?? null)
  )
    throw new SlackAPIError('invalid_history_cursor')
  return parsed.data.before
}

function compareTimestamp(a: string, b: string): number {
  const [secondsA = '', microsA = ''] = a.split('.')
  const [secondsB = '', microsB = ''] = b.split('.')
  const left = BigInt(secondsA) * 1_000_000n + BigInt(microsA.padEnd(6, '0'))
  const right = BigInt(secondsB) * 1_000_000n + BigInt(microsB.padEnd(6, '0'))
  return left < right ? -1 : left > right ? 1 : 0
}
