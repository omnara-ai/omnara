import assert from 'node:assert/strict'
import test from 'node:test'

import type { ModelOutputDelta, ModelOutputStreamDelta } from '@omnara/sdk'

import { DeltaRenderer, resetConnectionScopedRendering } from './chat-rendering.js'

const modelCallContextId = `mcc_${'a'.repeat(26)}`

function delta(event: ModelOutputStreamDelta, contextId = modelCallContextId): ModelOutputDelta {
  return {
    turn_id: `trn_${'a'.repeat(26)}`,
    model_call_context_id: contextId,
    seq: 1,
    source_seq_start: 1,
    source_seq_end: 1,
    coalesced_count: 1,
    event,
  }
}

class FakeTerminal {
  readonly printed: string[] = []
  readonly previews: (string | undefined)[] = []

  printBlock(text: string): void {
    this.printed.push(text)
  }

  setPreview(text: string | undefined): void {
    this.previews.push(text)
  }
}

test('reconnect discards partial output and stale thinking detail', () => {
  const terminal = new FakeTerminal()
  const renderer = new DeltaRenderer(terminal, 'agent', 'error')

  renderer.handle(
    delta({
      kind: 'text_delta',
      block_index: 0,
      delta: `${'long partial output '.repeat(10)}\nsecond line`,
    }),
  )
  assert.deepEqual(terminal.printed, [])
  assert.match(terminal.previews.at(-1) ?? '', /^agent …/)

  const state = resetConnectionScopedRendering(renderer, {
    text: 'thinking…',
    detail: 'stale reasoning',
  })

  assert.deepEqual(state, { text: 'thinking…' })
  assert.equal(terminal.previews.at(-1), undefined)
  assert.deepEqual(terminal.printed, [])

  renderer.handle(delta({ kind: 'text_delta', block_index: 0, delta: 'complete answer' }))
  renderer.handle(delta({ kind: 'block_stop', block_index: 0 }))
  assert.deepEqual(terminal.printed, [])
  assert.equal(terminal.previews.at(-1), undefined)
})

test('durable completion clears only its matching preview context', () => {
  const terminal = new FakeTerminal()
  const renderer = new DeltaRenderer(terminal, 'agent', 'error')

  renderer.handle(
    delta({ kind: 'text_delta', block_index: 0, delta: 'newer preview' }, 'newer-model-call'),
  )
  renderer.complete('older-model-call')
  assert.equal(terminal.previews.at(-1), 'agent newer preview')

  renderer.complete('newer-model-call')
  assert.equal(terminal.previews.at(-1), undefined)
  assert.deepEqual(terminal.printed, [])
})
