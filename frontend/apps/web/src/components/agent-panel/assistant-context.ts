import { useCreateAssistantAgent } from '@omnara/react'
import type { AgentPanelScope, AgentPanelSetup } from '@omnara/sdk'
import { createContext, useEffect, useRef } from 'react'

import type { PagePointer } from './page-actions'

export interface AssistantBootstrap {
  scope: AgentPanelScope
  model: AgentPanelSetup['model']
  pending: boolean
  error: Error | null
  retry: () => void
}

export type AssistantSender = (text: string) => Promise<void>

export interface AssistantSurface {
  bootstrap: AssistantBootstrap | null
  open: () => void
  ask: (text: string) => void
  registerSender: (sender: AssistantSender | null) => void
  /** A page offers a place for the conversation to live in; null takes it back. */
  setDock: (element: HTMLElement | null) => void
  /** A page that is itself the way to the assistant hides the corner launcher. */
  hideLauncher: (hidden: boolean) => void
  /** Marks something on the page for the user; the corner panel steps aside if it covers it. */
  point: (pointer: PagePointer | null) => void
  /** Keeps the conversation open through a move to another page that the assistant makes. */
  follow: () => void
}

/** Holds questions asked before the conversation can take them, and sends them in order. */
export function createAskQueue() {
  let sender: AssistantSender | null = null
  const waiting: ((sender: AssistantSender) => void)[] = []
  let asked = Promise.resolve()
  return {
    register: (next: AssistantSender | null) => {
      sender = next
      if (next == null) return
      for (const resolve of waiting.splice(0)) resolve(next)
    },
    ask: (text: string) => {
      asked = asked.then(async () => {
        const send =
          sender ??
          (await new Promise<AssistantSender>((resolve) => {
            waiting.push(resolve)
          }))
        await send(text)
      })
    },
  }
}

export const AssistantContext = createContext<AssistantSurface>({
  bootstrap: null,
  open: () => undefined,
  ask: () => undefined,
  registerSender: () => undefined,
  setDock: () => undefined,
  hideLauncher: () => undefined,
  point: () => undefined,
  follow: () => undefined,
})

function automaticRequest(setup: AgentPanelSetup, revision: number) {
  return {
    orgID: setup.scope.org_id,
    body: {
      project_id: setup.scope.project_id,
      request_id: setup.request_id,
      expected_revision: revision,
      automatic: true,
    },
  }
}

export function useAutomaticAssistant(
  setup: AgentPanelSetup | undefined,
  revision: number,
  onCreated: () => void,
): AssistantBootstrap | null {
  const { mutate, isPending, error } = useCreateAssistantAgent()
  const attempted = useRef<string | null>(null)
  useEffect(() => {
    if (setup?.model !== 'available') return
    const key = `${setup.request_id}:${String(revision)}`
    if (attempted.current === key) return
    attempted.current = key
    mutate(automaticRequest(setup, revision), { onSuccess: onCreated })
  }, [mutate, onCreated, revision, setup])

  if (setup == null) return null
  return {
    scope: setup.scope,
    model: setup.model,
    pending: isPending,
    error,
    retry: () => {
      mutate(automaticRequest(setup, revision), { onSuccess: onCreated })
    },
  }
}
