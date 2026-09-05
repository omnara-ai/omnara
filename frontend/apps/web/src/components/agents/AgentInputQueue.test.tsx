/** @vitest-environment happy-dom */

import type { UseAgentChatResult } from '@omnara/react'
import type { AgentInput } from '@omnara/sdk'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { AgentInputQueue } from '@/components/agents/AgentInputQueue'
import { enableReactActEnvironment } from '@/test/react-act'

const cancel = vi.fn()
const promote = vi.fn()
const move = vi.fn()
const beginCancellation = vi.fn()

function backlog(
  inputs: UseAgentChatResult['inputBacklog']['inputs'],
): UseAgentChatResult['inputBacklog'] {
  return { inputs, actionPending: false, beginCancellation, cancel, promote, move }
}

const queueProps = {
  canOperate: true,
  canSendNow: true,
}

function input(
  id: string,
  text: string,
  deliveryMode: AgentInput['delivery_mode'] = 'queued',
): AgentInput {
  return {
    id,
    agent_id: 'agent',
    state: 'received',
    delivery_mode: deliveryMode,
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
  beforeEach(() => {
    vi.clearAllMocks()
    cancel.mockResolvedValue(undefined)
    promote.mockResolvedValue(undefined)
    move.mockResolvedValue(undefined)
  })

  it('renders inputs in authoritative backlog order', () => {
    const firstQueued = input('input-1', 'Raw message')
    firstQueued.content_blocks = [
      { type: 'text', text: 'Hidden web context', metadata: { omnara_hidden: 'true' } },
      { type: 'text', text: 'Raw message', metadata: { omnara_display_text: 'First queued' } },
      { type: 'media_ref', artifact_id: 'artifact-with-text' },
    ]
    const steering = input('input-2', 'Send me', 'steering')
    const attachment = input('input-3', '')
    attachment.content_blocks = [
      { type: 'media_ref', artifact_id: 'artifact-1' },
      { type: 'media_ref', artifact_id: 'artifact-2' },
    ]

    const html = renderToStaticMarkup(
      <AgentInputQueue {...queueProps} backlog={backlog([steering, firstQueued, attachment])} />,
    )

    expect(html.indexOf('Send me')).toBeLessThan(html.indexOf('First queued'))
    expect(html).toContain('2 attachments')
    expect(html.match(/lucide-file/g)).toHaveLength(2)
    expect(html).toContain('Now')
    expect(html).toContain('Next')
    expect(html).toContain('>2</span>')
    expect(html).not.toContain('Raw message')
    expect(html).not.toContain('Hidden web context')
    expect(html.match(/Sending…/g)).toHaveLength(1)
    expect(html.match(/Send now/g)).toHaveLength(2)
    expect(html.match(/title="Drag to reorder"/g)).toHaveLength(3)
  })

  it('preserves the send-action width when sending state changes', () => {
    const queued = renderToStaticMarkup(
      <AgentInputQueue {...queueProps} backlog={backlog([input('input-1', 'Message')])} />,
    )
    const steering = renderToStaticMarkup(
      <AgentInputQueue
        {...queueProps}
        backlog={backlog([input('input-1', 'Message', 'steering')])}
      />,
    )

    expect(queued).toContain('w-16')
    expect(steering).toContain('w-16')
    expect(queued).toContain('Send now')
    expect(steering).toContain('Sending…')
  })

  it('renders a pending busy send without queue actions', () => {
    const html = renderToStaticMarkup(
      <AgentInputQueue
        {...queueProps}
        backlog={backlog([
          { id: 'key-1', delivery_mode: 'optimistic', text: 'Message', attachmentCount: 1 },
        ])}
      />,
    )

    expect(html).toContain('Sending…')
    expect(html).not.toContain('Send now')
    expect(html).toContain('disabled')
    expect(html).toContain('lucide-file')
  })

  it('keeps multiple steering inputs in their authoritative FIFO order', () => {
    const html = renderToStaticMarkup(
      <AgentInputQueue
        {...queueProps}
        backlog={backlog([
          input('input-1', 'First steering', 'steering'),
          input('input-2', 'Second steering', 'steering'),
          input('input-3', 'Queued'),
        ])}
      />,
    )

    expect(html.indexOf('First steering')).toBeLessThan(html.indexOf('Second steering'))
    expect(html.indexOf('Second steering')).toBeLessThan(html.indexOf('Queued'))
  })

  it('hides send-now actions when steering is unavailable', () => {
    const html = renderToStaticMarkup(
      <AgentInputQueue
        {...queueProps}
        canSendNow={false}
        backlog={backlog([input('input-1', 'Message')])}
      />,
    )

    expect(html).not.toContain('Send now')
    expect(html).toContain('Remove queued message')
  })

  it('promotes the selected queued input', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)

    try {
      act(() => {
        root.render(
          <AgentInputQueue
            {...queueProps}
            backlog={backlog([input('input-1', 'First'), input('input-2', 'Second')])}
          />,
        )
      })
      const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>('button')).filter(
        (button) => button.textContent === 'Send now',
      )
      await act(async () => {
        buttons[1]?.click()
        await Promise.resolve()
      })

      expect(promote).toHaveBeenCalledWith('input-2')
    } finally {
      act(() => {
        root.unmount()
      })
      container.remove()
    }
  })

  it('moves a queued row relative to its queued drop target', async () => {
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    let resolveMove!: () => void
    move.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          resolveMove = resolve
        }),
    )
    const restoreActEnvironment = enableReactActEnvironment()

    try {
      act(() => {
        root.render(
          <AgentInputQueue
            {...queueProps}
            backlog={backlog([
              input('input-0', 'Sending', 'steering'),
              input('input-1', 'First'),
              input('input-2', 'Second'),
              input('input-3', 'Third'),
            ])}
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

      expect(move).toHaveBeenCalledWith({
        inputID: 'input-3',
        anchorInputID: 'input-2',
        position: 'before',
      })
      expect(Array.from(container.querySelectorAll('p')).map((row) => row.textContent)).toEqual([
        'Sending',
        'First',
        'Third',
        'Second',
      ])

      act(() => {
        root.render(
          <AgentInputQueue
            {...queueProps}
            backlog={backlog([
              input('input-0', 'Sending', 'steering'),
              input('input-1', 'First'),
              input('input-3', 'Third'),
              input('input-2', 'Second'),
            ])}
          />,
        )
      })
      expect(Array.from(container.querySelectorAll('p')).map((row) => row.textContent)).toEqual([
        'Sending',
        'First',
        'Third',
        'Second',
      ])

      await act(async () => {
        resolveMove()
        await Promise.resolve()
      })
    } finally {
      act(() => {
        root.unmount()
      })
      container.remove()
      restoreActEnvironment()
    }
  })
})
