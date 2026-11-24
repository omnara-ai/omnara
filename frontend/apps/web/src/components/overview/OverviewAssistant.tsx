import type { CurrentUserOrg } from '@omnara/sdk'
import { useContext } from 'react'

import { AssistantContext } from '@/components/agent-panel/assistant-context'
import { Button } from '@/components/ui/button'

/**
 * The Overview's main column when an assistant is connected: its conversation lives in
 * the page here, not in the corner. Inline-size containment keeps a long unbroken line
 * in the conversation from widening the page.
 */
export function AssistantHome() {
  const { setDock } = useContext(AssistantContext)
  return (
    <section
      ref={setDock}
      aria-label="Assistant"
      className="bg-card flex h-[70svh] min-h-0 min-w-0 flex-col overflow-hidden rounded-xl border contain-inline-size lg:h-auto lg:flex-1"
    />
  )
}

export function AssistantInvite({ org }: { org: CurrentUserOrg }) {
  const { open } = useContext(AssistantContext)
  return (
    <section className="bg-card flex items-center justify-between gap-4 rounded-xl border p-5">
      <div className="min-w-0">
        <h3 className="text-sm font-medium">Assistant</h3>
        <p className="text-muted-foreground text-sm">Not set up</p>
      </div>
      <Button size="sm" onClick={open}>
        {org.role === 'owner' ? 'Set up' : 'Connect'}
      </Button>
    </section>
  )
}
