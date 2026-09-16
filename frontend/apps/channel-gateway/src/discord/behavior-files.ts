import { isUtf8 } from 'node:buffer'

import { type CreateAgentInputContentBlock, schemas } from '@omnara/sdk'
import { z } from 'zod'

import type { OperationAttemptContext } from '../operations/retry'
import type { ProviderWorkReservation } from '../types'
import { discordAPIURL } from './bootstrap'
import { discordRequest } from './client'
import type { DiscordConfiguration } from './configuration'
import { type DiscordAttachment, discordAttachment, type DiscordInboundMessage } from './events'
import { DiscordAPIError, discordID, providerValue } from './protocol'

const maxItemBytes = 10 * 1024 * 1024
const maxTotalBytes = 24 * 1024 * 1024

/** Fresh native message reads renew signed CDN URLs. Preserve the captured
 * content/attachment identity; only URLs are refreshed, never edited text.
 * Hold actual-byte reservations through core's input serialization/commit.
 */
export async function prepareDiscordFiles(
  event: DiscordInboundMessage,
  configuration: DiscordConfiguration,
  attempt: OperationAttemptContext,
  work: ProviderWorkReservation,
  apiUrl?: string,
): Promise<CreateAgentInputContentBlock[]> {
  const api = discordAPIURL(apiUrl)
  const refreshed = providerValue(
    z.object({
      id: discordID,
      channel_id: discordID,
      guild_id: discordID.optional(),
      attachments: z.array(discordAttachment).max(100),
    }),
    await discordRequest(
      api,
      configuration.botToken,
      'GET',
      `channels/${event.channel_id}/messages/${event.id}`,
      attempt,
    ),
  )
  if (
    refreshed.id !== event.id ||
    refreshed.channel_id !== event.channel_id ||
    (refreshed.guild_id !== undefined && refreshed.guild_id !== configuration.guildID)
  )
    throw new DiscordAPIError('attachment_scope_mismatch')
  const byID = new Map(refreshed.attachments.map((file) => [file.id, file]))
  const blocks: CreateAgentInputContentBlock[] = []
  let retained = 0
  for (const file of event.attachments.slice(0, 20)) {
    attempt.signal.throwIfAborted()
    const current = byID.get(file.id)
    const mime = schemas.zInlineMediaContentBlock.shape.media_type.safeParse(file.content_type)
    const limit = Math.min(maxItemBytes, maxTotalBytes - retained)
    let reason: string | undefined
    if (!current) reason = 'attachment no longer available'
    else if (!mime.success) reason = 'unsupported media type'
    else if (file.size > limit || limit <= 0) reason = 'attachment size limit'
    else {
      try {
        const url = attachmentURL(current, event.channel_id, api)
        const bytes = await download(url, limit, attempt.signal, (size) => {
          work.resize((retained + size) * 8 + 1024 * 1024)
        })
        if (!bytes.length) reason = 'empty attachment'
        else if (mime.data.startsWith('text/') && !isUtf8(bytes)) reason = 'invalid text encoding'
        else {
          blocks.push({
            type: 'media',
            media_type: mime.data,
            data: bytes.toString('base64'),
            filename:
              Buffer.byteLength(file.filename) <= 255 && !file.filename.includes('\0')
                ? file.filename
                : undefined,
          })
          retained += bytes.length
        }
      } catch (error) {
        attempt.signal.throwIfAborted()
        if (!(error instanceof DiscordAPIError) || error.retryable) throw error
        reason = error.code
      } finally {
        work.resize(retained * 8 + 1024 * 1024)
      }
    }
    if (reason) blocks.push({ type: 'text', text: `[Discord attachment ${file.id}: ${reason}]` })
  }
  if (event.attachments.length > 20)
    blocks.push({
      type: 'text',
      text: `[${event.attachments.length - 20} additional Discord attachments omitted.]`,
    })
  return blocks
}

function attachmentURL(file: DiscordAttachment, channelID: string, api: URL): URL {
  let url: URL
  try {
    url = new URL(file.url)
  } catch {
    throw new DiscordAPIError('invalid_attachment_url')
  }
  const local = api.protocol === 'http:' && url.origin === api.origin
  if (
    url.username ||
    url.password ||
    url.hash ||
    (!local &&
      (url.protocol !== 'https:' ||
        url.port ||
        !['cdn.discordapp.com', 'media.discordapp.net'].includes(url.hostname))) ||
    !url.pathname.startsWith(`/attachments/${channelID}/${file.id}/`)
  )
    throw new DiscordAPIError('invalid_attachment_url')
  return url
}

async function download(
  url: URL,
  limit: number,
  signal: AbortSignal,
  reserve: (bytes: number) => void,
) {
  let response: Response
  try {
    response = await fetch(url, { signal, redirect: 'error' })
  } catch {
    throw new DiscordAPIError('attachment_unavailable', { retryable: true })
  }
  const reader = response.body?.getReader()
  try {
    if (!response.ok)
      throw new DiscordAPIError('attachment_unavailable', {
        retryable: response.status === 429 || response.status >= 500 || response.status === 408,
      })
    const declared = response.headers.get('content-length')
    if (declared && /^\d+$/.test(declared) && BigInt(declared) > BigInt(limit))
      throw new DiscordAPIError('attachment_size_limit')
    const chunks: Uint8Array[] = []
    let bytes = 0
    if (!reader) return Buffer.alloc(0)
    for (;;) {
      const next = await reader.read()
      if (next.done) return Buffer.concat(chunks, bytes)
      bytes += next.value.length
      if (bytes > limit) throw new DiscordAPIError('attachment_size_limit')
      reserve(bytes)
      chunks.push(next.value)
    }
  } catch (error) {
    if (error instanceof DiscordAPIError) throw error
    throw new DiscordAPIError('attachment_unavailable', { retryable: true })
  } finally {
    await reader?.cancel().catch(() => undefined)
  }
}
