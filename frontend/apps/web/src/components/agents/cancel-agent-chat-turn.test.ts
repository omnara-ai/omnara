import type { UseAgentChatResult } from '@omnara/react'
import type { AgentInput, CancelAgentResponse } from '@omnara/sdk'
import { expect, it, vi } from 'vitest'

import { cancelAgentChatTurn } from './cancel-agent-chat-turn'

function fixture() {
  const steering: AgentInput = {
    id: 'steering',
    agent_id: 'agent',
    state: 'received',
    delivery_mode: 'steering',
    input_kind: 'content',
    content_blocks: [{ type: 'text', text: 'Next instruction' }],
    queued_at: '2026-11-21T00:00:00Z',
  }
  const rollback = vi.fn()
  const inputBacklog: UseAgentChatResult['inputBacklog'] = {
    inputs: [
      steering,
      { ...steering, id: 'queued', delivery_mode: 'queued' },
      { ...steering, id: 'scheduled', state: 'scheduled', scheduled_for: '2026-12-01T00:00:00Z' },
      { id: 'optimistic', delivery_mode: 'optimistic', text: 'Sending', attachmentCount: 0 },
    ],
    actionPending: false,
    beginCancellation: vi.fn().mockResolvedValue(rollback),
    cancel: vi.fn(),
    promote: vi.fn(),
    move: vi.fn(),
  }
  const cancel = vi.fn<() => Promise<CancelAgentResponse>>().mockResolvedValue({
    event: null,
    runtime_cancel_requested: true,
    affected: true,
  })
  return { chat: { inputBacklog }, cancel, rollback }
}

it('stops the current turn without canceling scheduled, queued, or sending inputs', async () => {
  const { chat, cancel, rollback } = fixture()

  await cancelAgentChatTurn(chat, cancel)

  expect(chat.inputBacklog.beginCancellation).toHaveBeenCalledExactlyOnceWith(['steering'])
  expect(cancel).toHaveBeenCalledOnce()
  expect(rollback).not.toHaveBeenCalled()
})

it('restores the steering input if stopping the turn fails', async () => {
  const { chat, cancel, rollback } = fixture()
  const error = new Error('Could not stop the turn')
  cancel.mockRejectedValue(error)

  await expect(cancelAgentChatTurn(chat, cancel)).rejects.toBe(error)

  expect(chat.inputBacklog.beginCancellation).toHaveBeenCalledExactlyOnceWith(['steering'])
  expect(rollback).toHaveBeenCalledOnce()
})
