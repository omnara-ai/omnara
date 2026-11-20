import { expect, it } from 'vitest'

import { formatDuration, parseTrace } from './otlpTrace'

const second = 1_000_000_000n
const base = 1_794_830_400n * second

function span(id: string, parent: string | undefined, name: string, from: bigint, to: bigint) {
  return {
    traceId: 'a'.repeat(32),
    spanId: id,
    parentSpanId: parent,
    name,
    startTimeUnixNano: String(base + from * second),
    endTimeUnixNano: String(base + to * second),
    attributes: [],
  }
}

it('orders spans under their parent by start time and measures them against the trace', () => {
  const trace = parseTrace({
    resourceSpans: [
      {
        scopeSpans: [
          {
            spans: [
              {
                ...span('root', undefined, 'invoke_agent researcher', 0n, 8n),
                attributes: [
                  { key: 'gen_ai.operation.name', value: { stringValue: 'invoke_agent' } },
                ],
              },
              {
                ...span('tool', 'root', 'execute_tool search', 4n, 6n),
                attributes: [
                  { key: 'gen_ai.operation.name', value: { stringValue: 'execute_tool' } },
                ],
                status: { code: 2, message: 'timed out' },
                links: [
                  {
                    traceId: 'b'.repeat(32),
                    spanId: 'c'.repeat(16),
                    attributes: [
                      { key: 'omnara.link.kind', value: { stringValue: 'spawned' } },
                      { key: 'omnara.link.agent.id', value: { stringValue: 'agt_child' } },
                      { key: 'omnara.link.turn.id', value: { stringValue: 'trn_child' } },
                    ],
                  },
                ],
              },
              {
                ...span('chat', 'root', 'chat claude', 2n, 4n),
                attributes: [
                  { key: 'gen_ai.operation.name', value: { stringValue: 'chat' } },
                  { key: 'gen_ai.usage.input_tokens', value: { intValue: '100' } },
                  {
                    key: 'gen_ai.response.finish_reasons',
                    value: { arrayValue: { values: [{ stringValue: 'tool_use' }] } },
                  },
                ],
              },
            ],
          },
        ],
      },
    ],
  })
  expect(trace?.durationMs).toBe(8000)
  expect(trace?.spans.map((item) => [item.id, item.role, item.depth, item.offsetMs])).toEqual([
    ['root', 'agent', 0, 0],
    ['chat', 'model', 1, 2000],
    ['tool', 'tool', 1, 4000],
  ])
  const [root, chat, tool] = trace?.spans ?? []
  expect(root?.startedAt.toISOString()).toBe('2026-11-16T12:00:00.000Z')
  expect(chat?.durationMs).toBe(2000)
  expect(chat?.attributes.slice(1)).toEqual([
    { key: 'gen_ai.usage.input_tokens', value: '100' },
    { key: 'gen_ai.response.finish_reasons', value: 'tool_use' },
  ])
  expect([tool?.error, tool?.statusMessage, root?.error]).toEqual([true, 'timed out', false])
  expect([root?.links, tool?.links]).toEqual([
    [],
    [
      {
        kind: 'spawned',
        agentId: 'agt_child',
        turnId: 'trn_child',
        traceId: 'b'.repeat(32),
        spanId: 'c'.repeat(16),
      },
    ],
  ])
})

it('keeps a span whose parent is missing at the top level', () => {
  const trace = parseTrace({
    resourceSpans: [{ scopeSpans: [{ spans: [span('orphan', 'gone', 'chat', 0n, 1n)] }] }],
  })
  expect(trace?.spans.map((item) => [item.id, item.depth])).toEqual([['orphan', 0]])
})

it('reads a trace with no spans and rejects other payloads', () => {
  expect(parseTrace({ resourceSpans: [{ scopeSpans: [{}] }] })).toEqual({
    spans: [],
    durationMs: 0,
  })
  expect(parseTrace({ resourceSpans: [{ scopeSpans: 'none' }] })).toBeUndefined()
})

it('formats durations at a readable precision', () => {
  expect([0.2, 340, 2340, 12_340, 125_000].map(formatDuration)).toEqual([
    '<1 ms',
    '340 ms',
    '2.34 s',
    '12.3 s',
    '2m 5s',
  ])
})
