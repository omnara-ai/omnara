/** @vitest-environment happy-dom */

import type { AgentInput } from '@omnara/sdk'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'

import { AgentInputQueue } from '@/components/agents/AgentInputQueue'

function input(id: string, text: string): AgentInput {
  return {
    id,
    agent_id: 'agent',
    state: 'received',
    delivery_mode: 'queued',
    input_kind: 'content',
    content_blocks: [
      {
        type: 'text',
        text: 'Hidden web context',
        metadata: { omnara_hidden: 'true' },
      },
      { type: 'text', text },
    ],
    queued_at: '2026-08-20T00:00:00Z',
  }
}

describe('AgentInputQueue', () => {
  it('renders every queued message with its queue order, preview, and actions', () => {
    const firstInput = input('input-1', 'Raw message')
    firstInput.content_blocks = [
      { type: 'text', text: 'Hidden web context', metadata: { omnara_hidden: 'true' } },
      { type: 'text', text: 'Raw message', metadata: { omnara_display_text: 'First message' } },
    ]
    const secondInput = input('input-2', '')
    secondInput.content_blocks = [{ type: 'media_ref', artifact_id: 'artifact' }]

    const html = renderToStaticMarkup(
      <AgentInputQueue
        inputs={[firstInput, secondInput]}
        canOperate
        canSendNow
        actionPending={false}
        onSendNow={vi.fn()}
        onRemove={vi.fn()}
        onMove={vi.fn()}
      />,
    )

    expect(html).toContain('First message')
    expect(html).not.toContain('Raw message')
    expect(html).toContain('Attachment')
    expect(html).toContain('Next')
    expect(html).toContain('>2</span>')
    expect(html).not.toContain('Hidden web context')
    expect(html.match(/Send now/g)).toHaveLength(2)
    expect(html.match(/Remove queued message/g)).toHaveLength(2)
    expect(html.match(/Reorder queued message/g)).toHaveLength(2)
  })

  it('hides send-now actions when steering is unavailable', () => {
    const html = renderToStaticMarkup(
      <AgentInputQueue
        inputs={[input('input-1', 'Message')]}
        canOperate
        canSendNow={false}
        actionPending={false}
        onSendNow={vi.fn()}
        onRemove={vi.fn()}
        onMove={vi.fn()}
      />,
    )

    expect(html).not.toContain('Send now')
    expect(html).toContain('Remove queued message')
    expect(html).not.toContain('Reorder queued message')
  })

  it('moves a row relative to its drop target', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    const onMove = vi.fn().mockResolvedValue(undefined)
    const actEnvironment = globalThis as typeof globalThis & {
      IS_REACT_ACT_ENVIRONMENT?: boolean
    }
    const previousActEnvironment = actEnvironment.IS_REACT_ACT_ENVIRONMENT
    actEnvironment.IS_REACT_ACT_ENVIRONMENT = true

    try {
      act(() => {
        root.render(
          <AgentInputQueue
            inputs={[
              input('input-1', 'First message'),
              input('input-2', 'Second message'),
              input('input-3', 'Third message'),
            ]}
            canOperate
            canSendNow
            actionPending={false}
            onSendNow={vi.fn()}
            onRemove={vi.fn()}
            onMove={onMove}
          />,
        )
      })

      const handles = Array.from(
        container.querySelectorAll<HTMLButtonElement>('[aria-label^="Reorder queued message"]'),
      )
      handles.forEach((candidate, index) => {
        const row = candidate.parentElement
        if (!row) throw new Error('Missing queued message row')
        row.getBoundingClientRect = () => ({
          x: 0,
          y: index * 40,
          top: index * 40,
          right: 600,
          bottom: index * 40 + 40,
          left: 0,
          width: 600,
          height: 40,
          toJSON: () => undefined,
        })
      })
      const handle = handles[2]
      if (!handle) throw new Error('Missing reorder handle')

      await act(async () => {
        handle.focus()
        handle.dispatchEvent(
          new KeyboardEvent('keydown', { key: ' ', code: 'Space', bubbles: true }),
        )
        await new Promise((resolve) => setTimeout(resolve, 0))
      })
      await act(async () => {
        handle.dispatchEvent(
          new KeyboardEvent('keydown', { key: 'ArrowUp', code: 'ArrowUp', bubbles: true }),
        )
        await new Promise((resolve) => setTimeout(resolve, 0))
      })
      await act(async () => {
        handle.dispatchEvent(
          new KeyboardEvent('keydown', { key: ' ', code: 'Space', bubbles: true }),
        )
        await new Promise((resolve) => setTimeout(resolve, 0))
      })

      expect(onMove).toHaveBeenCalledWith('input-3', 'input-2', 'before')
    } finally {
      act(() => {
        root.unmount()
      })
      container.remove()
      actEnvironment.IS_REACT_ACT_ENVIRONMENT = previousActEnvironment
    }
  })
})
