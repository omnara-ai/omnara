import { SlackAdapter } from '@chat-adapter/slack'
import { LogLevel, type WebAPICallResult } from '@slack/web-api'
import { ConsoleLogger } from 'chat'
import { z } from 'zod'

import { currentProviderOperation, withProviderOperation } from '../operations/provider-context'
import { type OperationAttemptContext, parseRetryAfter } from '../operations/retry'
import { publicCodes, SlackAPIError, transientCodes } from './errors'
import {
  slackEnvelope,
  type SlackMethod,
  type SlackRequest,
  type SlackResponse,
  slackResponses,
} from './protocol'

const ignoreSDKLog = (): void => undefined
const responseBytes = 1024 * 1024
// Axios uses ERR_BAD_RESPONSE for both oversized and interrupted responses.
// Match the pinned transport's size error specifically so interrupted reads can
// still retry. Never propagate the SDK error (which may contain credentials).
const oversizedResponse = z.object({
  code: z.literal('slack_webapi_request_error'),
  original: z.object({
    code: z.literal('ERR_BAD_RESPONSE'),
    message: z.literal(`maxContentLength size of ${responseBytes} exceeded`),
  }),
})

/** Messaging only: core verifies and persists callbacks before gateway replay.
 * Bare adapter operations need no Chat routing host or second deduplication store.
 */
export class SlackMessagingAdapter extends SlackAdapter {
  constructor(botToken: string, apiUrl: string) {
    super({
      botToken,
      apiUrl,
      webhookVerifier: () => false,
      logger: new ConsoleLogger('silent'),
      webClientOptions: {
        // SDK logs can contain provider bodies. Our operation boundary emits
        // sanitized diagnostics instead.
        logger: {
          debug: ignoreSDKLog,
          info: ignoreSDKLog,
          warn: ignoreSDKLog,
          error: ignoreSDKLog,
          setLevel: ignoreSDKLog,
          setName: ignoreSDKLog,
          getLevel: () => LogLevel.ERROR,
        },
        retryConfig: { retries: 0 },
        rejectRateLimitedCalls: true,
        // Omnara bounds concurrency. SDK queue continuations can inherit a
        // different request's async-local signal, so never queue here.
        maxRequestConcurrency: Infinity,
        allowAbsoluteUrls: false,
        requestInterceptor: (request) => {
          const context = currentProviderOperation()
          const publication =
            request.url?.endsWith('/chat.postMessage') === true ||
            request.url?.endsWith('/files.completeUploadExternal') === true
          request.signal = context.signal
          request.maxContentLength = responseBytes
          // Percent encoding can triple the JSON payload's byte count.
          request.maxBodyLength = 2 * 1024 * 1024
          request.maxRedirects = 0
          request.responseType = 'arraybuffer'
          request.transformResponse = [
            (bytes: Buffer, headers, status) => {
              if (status === 429)
                throw new SlackAPIError('ratelimited', {
                  retryable: true,
                  retryAfterMs:
                    parseRetryAfter(z.string().safeParse(headers['retry-after']).data ?? null) ??
                    Infinity,
                })
              if (status !== 200) {
                const transient = status !== undefined && (status >= 500 || status === 408)
                throw new SlackAPIError(transient ? 'provider_unavailable' : 'http_rejected', {
                  retryable: transient && !publication,
                  outcomeUnknown: transient && publication,
                })
              }
              let result: unknown
              try {
                result = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes))
              } catch {
                throw new SlackAPIError('invalid_response', {
                  outcomeUnknown: publication,
                  retryable: !publication,
                })
              }
              const envelope = slackEnvelope.safeParse(result)
              if (!envelope.success)
                throw new SlackAPIError('invalid_response', { outcomeUnknown: publication })
              if (!envelope.data.ok) {
                const code = envelope.data.error ?? ''
                if (code === 'ratelimited')
                  throw new SlackAPIError(code, { retryable: true, retryAfterMs: Infinity })
                if (transientCodes.has(code))
                  throw new SlackAPIError('provider_unavailable', {
                    outcomeUnknown: publication,
                    retryable: !publication,
                  })
                throw new SlackAPIError(publicCodes.has(code) ? code : 'provider_rejected', {
                  outcomeUnknown: publication && !publicCodes.has(code),
                })
              }
              return result
            },
          ]
          return request
        },
      },
    })
  }

  api<M extends SlackMethod>(
    method: M,
    payload: SlackRequest[M],
    context: OperationAttemptContext,
  ): Promise<SlackResponse<M>> {
    if (Buffer.byteLength(JSON.stringify(payload)) > 512 * 1024)
      throw new SlackAPIError('request_too_large')
    return this.call(method, context, () => this.webClient.apiCall(method, { ...payload }))
  }

  private async call<M extends SlackMethod>(
    method: M,
    context: OperationAttemptContext,
    work: () => Promise<WebAPICallResult>,
  ): Promise<SlackResponse<M>> {
    const publication = method === 'chat.postMessage' || method === 'files.completeUploadExternal'
    try {
      const result = await withProviderOperation(context, work)
      const parsed = slackResponses[method].safeParse(result)
      if (!parsed.success)
        throw new SlackAPIError('invalid_response', { outcomeUnknown: publication })
      // SAFETY: The method selects the same response schema and return type.
      return parsed.data as SlackResponse<M>
    } catch (error) {
      if (error instanceof SlackAPIError) throw error
      if (oversizedResponse.safeParse(error).success)
        throw new SlackAPIError('response_too_large', { outcomeUnknown: publication })
      throw new SlackAPIError('transport_failed', {
        outcomeUnknown: publication,
        retryable: !publication,
      })
    }
  }
}
