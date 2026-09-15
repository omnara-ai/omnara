/** Test-only Go HTTP journey entrypoint. Stdin (through EOF) supplies
 * {coreUrl, slackUrl, token, send?}; stdout contains {processed, send_result?}.
 * Bundle separately with esbuild; this file is not a gateway deployment entry.
 */
import { addAbortSignal } from 'node:stream'

import { schemas } from '@omnara/sdk'
import { z } from 'zod'

import { CoreClient } from '../core-client'
import { initialReceiptWorkBytes } from '../receipt-http'
import { createSlackGateway, slackCapability } from '../slack/gateway'
import { WorkByteBudget } from '../work-budget'

const localURL = z.url().refine((value) => {
  const url = new URL(value)
  return (
    url.protocol === 'http:' &&
    ['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname) &&
    !url.username &&
    !url.password &&
    !url.search &&
    !url.hash
  )
})
const inputSchema = z.strictObject({
  coreUrl: localURL,
  slackUrl: localURL,
  token: z.string().min(1),
  send: z
    .strictObject({
      request_id: z.string().min(1),
      scope: z.strictObject({
        project_id: schemas.zProjectId,
        integration_app_id: schemas.zIntegrationAppId,
        integration_install_id: schemas.zIntegrationInstallId,
        agent_id: schemas.zAgentId,
        channel_id: schemas.zIntegrationTargetId,
      }),
      payload: schemas.zChannelSendOperation.strict(),
    })
    .optional(),
})

const controller = new AbortController()
const deadlineMs = Date.now() + 45_000
let phase = 'configuration'
const watchdog = setTimeout(() => {
  controller.abort()
  process.stderr.write(`Slack journey timed out during ${phase}\n`)
  process.exit(1)
}, 45_000)

async function main(): Promise<void> {
  const chunks: Buffer[] = []
  let bytes = 0
  for await (const chunk of addAbortSignal(controller.signal, process.stdin)) {
    const data = z.instanceof(Buffer).parse(chunk)
    bytes += data.length
    if (bytes > 64 * 1024) throw new Error('journey configuration too large')
    chunks.push(data)
  }
  const input = inputSchema.parse(JSON.parse(Buffer.concat(chunks).toString('utf8')))
  const core = new CoreClient({
    baseUrl: input.coreUrl,
    token: input.token,
    requestTimeoutMs: 10_000,
  })
  const workBudget = new WorkByteBudget(256 * 1024 * 1024)
  const slack = createSlackGateway({ core, workBudget, apiUrl: input.slackUrl })
  let sendResult
  if (input.send) {
    phase = 'send'
    const result = await slack.executeOperation(
      {
        kind: 'send',
        capability: slackCapability,
        requestId: input.send.request_id,
        deadlineMs,
        scope: input.send.scope,
        payloadJSON: JSON.stringify(input.send.payload),
        artifacts: [],
      },
      [],
      controller.signal,
    )
    sendResult = { request_id: input.send.request_id, ...result }
  }
  let processed = 0
  for (;;) {
    controller.signal.throwIfAborted()
    const work = workBudget.reserve(initialReceiptWorkBytes)
    try {
      phase = 'claim'
      const receipt = await core.claimNextEvent(slackCapability, 60_000, controller.signal, work)
      if (!receipt) break
      phase = 'behavior'
      // Leave completion time within both the actual lease and this process's
      // total deadline. Never reconstruct or alter the claimed receipt/input.
      const behaviorDeadlineMs = Math.min(deadlineMs, Date.parse(receipt.lease_expires_at)) - 5000
      if (!Number.isFinite(behaviorDeadlineMs) || behaviorDeadlineMs <= Date.now())
        throw new Error('insufficient receipt lease')
      await slack.processReceipt(receipt, {
        deadlineMs: behaviorDeadlineMs,
        signal: controller.signal,
      })
      phase = 'completion'
      await core.completeEvent(receipt, { state: 'completed' }, controller.signal)
      processed += 1
    } finally {
      work.release()
    }
  }
  if (workBudget.usedBytes !== 0) throw new Error('journey work reservation leaked')
  process.stdout.write(`${JSON.stringify({ processed, send_result: sendResult })}\n`)
}

void main()
  .catch(() => {
    // Test failures identify the phase without printing credentials or payloads.
    process.stderr.write(`Slack journey failed during ${phase}\n`)
    process.exitCode = 1
  })
  .finally(() => {
    clearTimeout(watchdog)
    controller.abort()
  })
