import {
  OperationRetryError,
  type OperationRetryOptions,
  retryOperation,
} from '../operations/retry'
import { type SlackClient, type SlackUpload } from './client'
import { SlackAPIError } from './errors'
import { type SlackRequest, type SlackResponse, slackTimestamp } from './protocol'

export interface SlackDestination {
  channel: string
  threadTs?: string
}

export interface SlackSendInput extends SlackDestination {
  text?: string
  artifacts?: readonly SlackUpload[]
}

/** Provider facts only. Core owns public IDs, continuation registration and grants. */
export interface SlackPublication extends SlackDestination {
  publication: 'published'
  messageTs?: string
  fileIds: string[]
}

export async function sendSlackMessage(
  client: SlackClient,
  input: SlackSendInput,
  operation: Omit<OperationRetryOptions, 'idempotent'>,
): Promise<SlackPublication> {
  validateDestination(input)
  const files = input.artifacts ?? []
  if (
    files.length > 20 ||
    (!input.text?.trim() && files.length === 0) ||
    (input.text !== undefined &&
      (Buffer.byteLength(input.text) > 64 * 1024 || Array.from(input.text).length > 40_000))
  ) {
    throw new SlackAPIError('invalid_message')
  }
  for (const file of files) {
    if (
      !file.filename ||
      Buffer.byteLength(file.filename) > 255 ||
      ['\r', '\n', '\u0000'].some((character) => file.filename.includes(character)) ||
      !Number.isSafeInteger(file.sizeBytes) ||
      file.sizeBytes < 0
    )
      throw new SlackAPIError('invalid_artifact')
  }
  // Keep successful staging across safe retries. Never repeat a publication after
  // an unknown outcome. Uncompleted Slack upload tickets expire without sharing.
  const uploaded: { id: string; title: string }[] = []
  const state = { publicationStarted: false }
  try {
    return await retryOperation(operation, async (context) => {
      while (uploaded.length < files.length) {
        const file = files[uploaded.length]
        if (!file) throw new SlackAPIError('invalid_artifact')
        const ticket = await client.api(
          'files.getUploadURLExternal',
          {
            filename: file.filename,
            length: file.sizeBytes,
          },
          context,
        )
        await client.upload(ticket.upload_url, file, context)
        uploaded.push({ id: ticket.file_id, title: file.filename })
      }
      context.signal.throwIfAborted()
      state.publicationStarted = true
      try {
        const [first, ...rest] = uploaded
        if (first) {
          let payload: SlackRequest['files.completeUploadExternal'] = {
            files: [first, ...rest],
            channel_id: input.channel,
            initial_comment: input.text ?? '',
          }
          if (input.threadTs)
            payload = { ...payload, channel_id: input.channel, thread_ts: input.threadTs }
          const result = await client.api('files.completeUploadExternal', payload, context)
          return {
            ...destination(input),
            publication: 'published',
            fileIds: uploaded.map((file) => file.id),
            messageTs: fileShareTimestamp(
              result,
              input,
              uploaded.map((file) => file.id),
            ),
          }
        }
        const payload: SlackRequest['chat.postMessage'] = {
          channel: input.channel,
          text: input.text ?? '',
        }
        if (input.threadTs) payload.thread_ts = input.threadTs
        const result = await client.api('chat.postMessage', payload, context)
        if (result.channel !== undefined && result.channel !== input.channel) {
          throw new SlackAPIError('invalid_publication_response', { outcomeUnknown: true })
        }
        if (result.response_metadata?.warnings?.includes('message_truncated')) {
          throw new SlackAPIError('publication_truncated', { outcomeUnknown: true })
        }
        return {
          ...destination(input),
          publication: 'published',
          messageTs: result.ts,
          fileIds: [],
        }
      } catch (error) {
        if (error instanceof SlackAPIError && !error.outcomeUnknown)
          state.publicationStarted = false
        throw error
      }
    })
  } catch (error) {
    if (error instanceof OperationRetryError && !state.publicationStarted && error.outcomeUnknown) {
      // The shared retry helper conservatively treats any in-flight work as an
      // ambiguous mutation. Upload staging has not published anything yet.
      throw new OperationRetryError(error.code, false, error.attempts)
    }
    throw error
  }
}

export function validateDestination(value: SlackDestination): void {
  if (
    !/^[A-Z][A-Z0-9]+$/.test(value.channel) ||
    value.channel.length > 128 ||
    (value.threadTs !== undefined && !slackTimestamp.safeParse(value.threadTs).success)
  ) {
    throw new SlackAPIError('invalid_destination')
  }
}

function destination(input: SlackDestination): SlackDestination {
  return { channel: input.channel, threadTs: input.threadTs }
}

function fileShareTimestamp(
  result: SlackResponse<'files.completeUploadExternal'>,
  target: SlackDestination,
  expectedFiles: string[],
): string | undefined {
  const timestamps = new Set<string>()
  if (!result.files || result.files.length !== expectedFiles.length) return undefined
  if (new Set(result.files.map((file) => file.id)).size !== expectedFiles.length) return undefined
  for (const file of result.files) {
    if (!file.shares || !expectedFiles.includes(file.id)) return undefined
    const fileTimestamps = new Set<string>()
    for (const visibility of ['public', 'private'] as const) {
      const channels = file.shares[visibility]
      if (!channels) continue
      const shares = channels[target.channel]
      if (!shares) continue
      for (const share of shares) {
        if ((share.thread_ts ?? undefined) === target.threadTs) fileTimestamps.add(share.ts)
      }
    }
    if (fileTimestamps.size !== 1) return undefined
    for (const timestamp of fileTimestamps) timestamps.add(timestamp)
  }
  return timestamps.size === 1 ? [...timestamps][0] : undefined
}
