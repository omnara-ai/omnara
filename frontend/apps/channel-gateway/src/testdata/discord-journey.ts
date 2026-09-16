/** Test-only Go HTTP/PG journey. Stdin supplies local URLs, token and an optional
 * native operation; stdout contains only {processed, operation_result?}.
 */
import { addAbortSignal } from 'node:stream'

import { schemas } from '@omnara/sdk'
import { z } from 'zod'

import { CoreClient } from '../core/client'
import { initialReceiptWorkBytes } from '../core/receipt-http'
import { processDiscordEvent } from '../discord/behavior'
import { createDiscordGateway, discordCapability } from '../discord/gateway'
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
const registrationScope = z.strictObject({
  project_id: schemas.zProjectId,
  integration_app_id: schemas.zIntegrationAppId,
  integration_install_id: schemas.zIntegrationInstallId,
})
const operation = z.discriminatedUnion('kind', [
  z.strictObject({
    kind: z.literal('resolve_address'),
    request_id: z.string().min(1),
    scope: registrationScope,
    payload: schemas.zChannelResolveAddressOperation.strict(),
  }),
  z.strictObject({
    kind: z.literal('send'),
    request_id: z.string().min(1),
    scope: registrationScope.extend({
      agent_id: schemas.zAgentId,
      channel_id: schemas.zIntegrationTargetId,
    }),
    payload: schemas.zChannelSendOperation.strict(),
  }),
])
const inputSchema = z.strictObject({
  coreUrl: localURL,
  discordUrl: localURL,
  token: z.string().min(1),
  operation: operation.optional(),
})

const controller = new AbortController()
const deadlineMs = Date.now() + 45_000
let phase = 'configuration'
const watchdog = setTimeout(() => {
  controller.abort()
  process.stderr.write(`Discord journey timed out during ${phase}\n`)
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
  const budget = new WorkByteBudget(256 * 1024 * 1024)
  if (input.operation) {
    phase = input.operation.kind
    const gateway = createDiscordGateway({ core, apiUrl: input.discordUrl })
    const shared = {
      capability: discordCapability,
      requestId: input.operation.request_id,
      deadlineMs,
      payloadJSON: JSON.stringify(input.operation.payload),
      artifacts: [],
    }
    const native =
      input.operation.kind === 'send'
        ? { ...shared, kind: input.operation.kind, scope: input.operation.scope }
        : { ...shared, kind: input.operation.kind, scope: input.operation.scope }
    const result = await gateway.executeOperation(native, [], controller.signal)
    process.stdout.write(
      `${JSON.stringify({
        processed: 0,
        operation_result: { request_id: native.requestId, ...result },
      })}\n`,
    )
    return
  }
  let processed = 0
  for (;;) {
    controller.signal.throwIfAborted()
    const work = budget.reserve(initialReceiptWorkBytes)
    try {
      phase = 'claim'
      const receipt = await core.claimNextEvent(discordCapability, 60_000, controller.signal, work)
      if (!receipt) break
      phase = 'behavior'
      const behaviorDeadlineMs = Math.min(deadlineMs, Date.parse(receipt.lease_expires_at)) - 5000
      if (!Number.isFinite(behaviorDeadlineMs) || behaviorDeadlineMs <= Date.now())
        throw new Error('insufficient receipt lease')
      await processDiscordEvent(receipt, {
        core,
        apiUrl: input.discordUrl,
        reserveWorkBytes: budget.reserve,
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
  if (budget.usedBytes !== 0) throw new Error('journey work reservation leaked')
  process.stdout.write(`${JSON.stringify({ processed })}\n`)
}

void main()
  .catch((cause: unknown) => {
    process.stderr.write(`Discord journey failed during ${phase}\n`)
    if (cause instanceof z.ZodError)
      process.stderr.write(
        `${JSON.stringify(cause.issues.map(({ path, code }) => ({ path, code })))}\n`,
      )
    process.exitCode = 1
  })
  .finally(() => {
    clearTimeout(watchdog)
    controller.abort()
  })
