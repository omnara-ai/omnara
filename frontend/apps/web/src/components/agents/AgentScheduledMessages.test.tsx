import type { AgentInput } from '@omnara/sdk'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { ScheduledMessagesList } from '@/components/agents/AgentScheduledMessages'

const scheduled: AgentInput & { state: 'scheduled' } = {
  id: 'input-1',
  agent_id: 'agent',
  state: 'scheduled',
  delivery_mode: 'queued',
  input_kind: 'content',
  content_blocks: [{ type: 'text', text: 'Check the deploy' }],
  queued_at: '2026-08-20T00:00:00Z',
  scheduled_for: '2026-08-21T00:00:00Z',
}

function render(canOperate: boolean): string {
  return renderToStaticMarkup(
    <ScheduledMessagesList
      scheduled={[scheduled]}
      canOperate={canOperate}
      cancelPending={false}
      onCancel={() => undefined}
    />,
  )
}

describe('ScheduledMessagesList', () => {
  it('lets an operator cancel a scheduled message', () => {
    const html = render(true)
    expect(html).toContain('Check the deploy')
    expect(html).toContain('Cancel scheduled message')
  })

  it('hides cancellation from a viewer who cannot operate the agent', () => {
    const html = render(false)
    expect(html).toContain('Check the deploy')
    expect(html).not.toContain('Cancel scheduled message')
  })
})
