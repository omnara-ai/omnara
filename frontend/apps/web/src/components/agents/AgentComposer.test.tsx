/** @vitest-environment happy-dom */

import type { UseAgentChatResult } from '@omnara/react'
import { ApiError } from '@omnara/sdk'
import { act, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'

import { MessageScrollerProvider } from '@/components/ui/message-scroller'
import { enableReactActEnvironment } from '@/test/react-act'

import { AgentComposer } from './AgentComposer'

const message =
  'This agent already has 50 pending messages. Wait for messages to be processed or cancel a pending message, then try again.'

type Send = UseAgentChatResult['sendMessage']

interface InFlightSend {
  settle?: (failure?: Error) => void
}

function Composer({ send }: { send: Send }) {
  const [error, setError] = useState<Error>()
  const chat: UseAgentChatResult = {
    events: [],
    messages: [],
    status: 'ready',
    isWorking: false,
    error,
    streamError: undefined,
    historyStatus: 'success',
    historyError: null,
    retryHistory: vi.fn(),
    reconnect: vi.fn(),
    hasOlderMessages: false,
    isLoadingOlderMessages: false,
    loadOlderMessages: vi.fn(),
    inputBacklog: {
      inputs: [],
      actionPending: false,
      beginCancellation: vi.fn(),
      cancel: vi.fn(),
      promote: vi.fn(),
      move: vi.fn(),
    },
    sendMessage: async (input) => {
      setError(undefined)
      try {
        await send(input)
      } catch (cause) {
        setError(cause instanceof Error ? cause : new Error(String(cause)))
        throw cause
      }
    },
  }
  return (
    <AgentComposer chat={chat} onCancel={vi.fn()} cancelPending={false} canOperate hidden={false} />
  )
}

async function withComposer(
  send: Send,
  run: (
    textarea: HTMLTextAreaElement,
    form: HTMLFormElement,
    container: HTMLElement,
  ) => Promise<void>,
) {
  const restore = enableReactActEnvironment()
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  try {
    act(() => {
      root.render(
        <MessageScrollerProvider>
          <Composer send={send} />
        </MessageScrollerProvider>,
      )
    })
    const textarea = container.querySelector('textarea')
    const form = container.querySelector('form')
    if (textarea == null || form == null) throw new Error('missing composer')
    await run(textarea, form, container)
  } finally {
    act(() => {
      root.unmount()
    })
    container.remove()
    restore()
  }
}

function type(textarea: HTMLTextAreaElement, value: string) {
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set?.call(
      textarea,
      value,
    )
    textarea.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function submit(form: HTMLFormElement) {
  await act(() => {
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    return Promise.resolve()
  })
}

it('shows the backlog error and restores the exact draft', async () => {
  const send = vi.fn<Send>().mockRejectedValueOnce(new ApiError(409, message, 'agent_backlog_full'))
  send.mockResolvedValueOnce(undefined)
  await withComposer(send, async (textarea, form, container) => {
    const draft = '  Please review this.\n\n'
    type(textarea, draft)
    await submit(form)
    expect(send).toHaveBeenCalledTimes(1)
    expect(textarea.value).toBe(draft)
    expect(container.querySelector('[role="alert"]')?.textContent).toBe(message)
    await submit(form)
    expect(send).toHaveBeenCalledTimes(2)
    expect(textarea.value).toBe('')
    expect(container.querySelector('[role="alert"]')).toBeNull()
  })
})

it('keeps text typed while a send is in flight', async () => {
  const pending: InFlightSend = {}
  const send = vi.fn<Send>(
    () =>
      new Promise<void>((resolve, reject) => {
        pending.settle = (failure) => {
          if (failure == null) {
            resolve()
          } else {
            reject(failure)
          }
        }
      }),
  )
  await withComposer(send, async (textarea, form) => {
    type(textarea, 'first')
    await submit(form)
    expect(textarea.value).toBe('')
    type(textarea, 'second')
    await act(async () => {
      pending.settle?.()
      await Promise.resolve()
    })
    expect(textarea.value).toBe('second')
    await submit(form)
    type(textarea, 'third')
    await act(async () => {
      pending.settle?.(new ApiError(409, message, 'agent_backlog_full'))
      await Promise.resolve()
    })
    expect(textarea.value).toBe('second\nthird')
  })
})
