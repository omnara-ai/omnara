import type { UseAgentChatResult } from '@omnara/react'
import type { CancelAgentResponse } from '@omnara/sdk'

export async function cancelAgentChatTurn(
  chat: Pick<UseAgentChatResult, 'inputBacklog'>,
  cancel: () => Promise<CancelAgentResponse>,
): Promise<void> {
  const ids = chat.inputBacklog.inputs
    .filter((input) => input.delivery_mode === 'steering')
    .map((input) => input.id)
  const rollback = await chat.inputBacklog.beginCancellation(ids)
  try {
    await cancel()
  } catch (error) {
    rollback()
    throw error
  }
}
