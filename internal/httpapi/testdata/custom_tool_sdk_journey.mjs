import assert from 'node:assert/strict'

import {
  bearerToken,
  createCustomToolWorker,
  createOmnaraClient,
  verifyEventWebhook,
} from '../../../frontend/packages/sdk/dist/index.js'

let input = ''
for await (const chunk of process.stdin) input += chunk
const config = JSON.parse(input)
const signal = AbortSignal.timeout(20_000)
const executions = new Map()
const worker = createCustomToolWorker({
  client: createOmnaraClient({
    baseUrl: config.baseUrl,
    auth: bearerToken(config.token),
    timeoutMs: 5_000,
  }),
  path: { orgID: config.orgID, projectID: config.projectID },
  tools: {
    async lookup_customer({ toolCallID, input }) {
      assert.deepEqual(input, { email: 'ada@example.com' })
      executions.set(toolCallID, (executions.get(toolCallID) ?? 0) + 1)
      return {
        outcome: 'succeeded',
        content_blocks: [
          { type: 'structured_data', value: { email: input.email, customer: 'Ada' } },
        ],
      }
    },
  },
})

const payload = await verifyEventWebhook(config.webhook)
assert.equal(payload.event, 'tool_call_update')
const outcomes = await Promise.all([
  worker.handle(payload.data, { signal }),
  worker.handle(payload.data, { signal }),
])
assert.deepEqual(outcomes, [{ status: 'completed' }, { status: 'completed' }])
assert.deepEqual(await worker.handle(payload.data, { signal }), { status: 'completed' })
assert.equal(executions.get(payload.data.tool_call_id), 1)
assert.equal(executions.has(config.recoveryCallID), false)

const recovered = await worker.check({ limit: 10, signal })
assert.equal(recovered.nextCursor, null)
assert.equal(recovered.results.length, 1)
assert.equal(recovered.results[0].call.toolCallID, config.recoveryCallID)
assert.deepEqual(recovered.results[0].outcome, { status: 'completed' })
assert.equal(executions.get(config.recoveryCallID), 1)
assert.equal(executions.size, 2)
