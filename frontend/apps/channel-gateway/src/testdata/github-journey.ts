/** Test-only local Go/TS journey. Bundled separately; no deployment entrypoint. */
import { addAbortSignal } from 'node:stream'

import { schemas } from '@omnara/sdk'
import { z } from 'zod'

import { CoreClient } from '../core-client'
import { createGitHubFactory } from '../github/factory'
import { createGitHubGateway, githubCapability } from '../github/gateway'
import { OperationRetryError } from '../operation-retry'
import { initialReceiptWorkBytes } from '../receipt-http'
import type { ProviderFactoryContext } from '../types'
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
  githubUrl: localURL,
  token: z.string().min(1),
  appID: schemas.zIntegrationAppId,
  processControl: z.boolean().default(false),
  webhooks: z
    .array(
      z.strictObject({
        event: z.string(),
        delivery: z.uuid(),
        signature: z.string(),
        body: z.string(),
      }),
    )
    .max(8)
    .default([]),
  operations: z
    .array(
      z.strictObject({
        kind: z.enum(['read', 'send']),
        request_id: schemas.zToolCallId,
        scope: z.strictObject({
          project_id: schemas.zProjectId,
          integration_app_id: schemas.zIntegrationAppId,
          integration_install_id: schemas.zIntegrationInstallId,
          agent_id: schemas.zAgentId,
          channel_id: schemas.zIntegrationTargetId,
        }),
        payload: z.union([schemas.zChannelSendOperation, schemas.zChannelReadOperation]),
      }),
    )
    .max(8)
    .default([]),
})
const controller = new AbortController()
const deadlineMs = Date.now() + 45_000
let phase = 'configuration'
const watchdog = setTimeout(() => {
  controller.abort()
  process.stderr.write(`GitHub journey timed out during ${phase}\n`)
  process.exit(1)
}, 45_000)
const unused = (): never => {
  throw new Error('GitHub must not use Chat state or interactions')
}
async function main() {
  const chunks: Buffer[] = []
  let bytes = 0
  for await (const chunk of addAbortSignal(controller.signal, process.stdin)) {
    const part = z.instanceof(Buffer).parse(chunk)
    bytes += part.length
    if (bytes > 1024 * 1024) throw new Error('journey input too large')
    chunks.push(part)
  }
  const input = inputSchema.parse(JSON.parse(Buffer.concat(chunks).toString('utf8')))
  const core = new CoreClient({ baseUrl: input.coreUrl, token: input.token })
  const budget = new WorkByteBudget(256 * 1024 * 1024)
  const configuration = await core.getAppConfiguration(input.appID, controller.signal)
  const context: ProviderFactoryContext = {
    configuration,
    reserveWorkBytes: budget.reserve,
    signal: controller.signal,
    getInstallation: (id) => core.getInstallationConfiguration(input.appID, id, controller.signal),
    resolveInstallation: (tenant, account) =>
      core.resolveInstallationConfiguration(input.appID, tenant, account, controller.signal),
    logger: { debug: unused, info: unused, warn: unused, error: unused },
  }
  const runtime = await createGitHubFactory({ core, apiUrl: input.githubUrl }).create(context)
  let processed = 0
  const statuses: number[] = []
  const results = []
  const controlResults = []
  try {
    for (const webhook of input.webhooks) {
      phase = 'webhook'
      const response = await runtime.handleWebhook(
        new Request('http://localhost/github', {
          method: 'POST',
          body: webhook.body,
          headers: {
            'content-type': 'application/json',
            'x-github-event': webhook.event,
            'x-github-delivery': webhook.delivery,
            'x-hub-signature-256': webhook.signature,
          },
        }),
        {
          reserveWorkBytes: budget.reserve,
          resolveInteraction: unused,
          submitInbound: (event, signal) => core.submitInbound(input.appID, event, signal),
        },
      )
      statuses.push(response.status)
      if (response.status !== 202) throw new Error('webhook not durably accepted')
    }
    for (;;) {
      phase = 'claim'
      const work = budget.reserve(initialReceiptWorkBytes)
      try {
        const receipt = await core.claimNextEvent(githubCapability, 60_000, controller.signal, work)
        if (!receipt) break
        if (++processed > 16) throw new Error('unexpected receipt fanout')
        phase = 'behavior'
        if (!runtime.processReceipt) throw new Error('missing behavior')
        await runtime.processReceipt(receipt, {
          signal: controller.signal,
          deadlineMs: Math.min(deadlineMs, Date.parse(receipt.lease_expires_at)) - 5000,
        })
        phase = 'completion'
        await core.completeEvent(receipt, { state: 'completed' }, controller.signal)
      } finally {
        work.release()
      }
    }
    const gateway = createGitHubGateway({ core, apiUrl: input.githubUrl })
    if (input.processControl) {
      phase = 'control claim'
      const controlBudget = new WorkByteBudget(128 * 1024 * 1024, budget)
      const work = controlBudget.reserve(initialReceiptWorkBytes)
      try {
        const receipt = await core.claimNextControlEvent(
          githubCapability,
          60_000,
          controller.signal,
          work,
        )
        if (receipt) {
          phase = 'control behavior'
          if (!runtime.processControlReceipt) throw new Error('missing control behavior')
          const completion = await runtime.processControlReceipt(receipt, {
            signal: controller.signal,
            deadlineMs: Math.min(deadlineMs, Date.parse(receipt.lease_expires_at)) - 5000,
            reserveWorkBytes: controlBudget.reserve,
          })
          phase = 'control completion'
          await core.completeControlEvent(receipt, completion, controller.signal)
          controlResults.push(completion)
        }
      } finally {
        work.release()
      }
      if (controlBudget.usedBytes) throw new Error('control reservation leaked')
    }
    for (const [index, operation] of input.operations.entries()) {
      phase = `${operation.kind} ${index + 1}`
      const result = await gateway.executeOperation(
        {
          kind: operation.kind,
          requestId: operation.request_id,
          scope: operation.scope,
          deadlineMs,
          capability: githubCapability,
          artifacts: [],
          payloadJSON: JSON.stringify(operation.payload),
        },
        [],
        controller.signal,
      )
      results.push({ request_id: operation.request_id, ...result })
    }
  } finally {
    await runtime.close()
  }
  if (budget.usedBytes) throw new Error('work reservation leaked')
  process.stdout.write(
    `${JSON.stringify({ processed, webhook_statuses: statuses, operation_results: results, control_results: controlResults })}\n`,
  )
}
void main()
  .catch((cause: unknown) => {
    const diagnostic =
      cause instanceof OperationRetryError
        ? `: ${cause.code}, attempts=${cause.attempts}, unknown=${cause.outcomeUnknown}`
        : ''
    process.stderr.write(`GitHub journey failed during ${phase}${diagnostic}\n`)
    process.exitCode = 1
  })
  .finally(() => {
    clearTimeout(watchdog)
    controller.abort()
  })
