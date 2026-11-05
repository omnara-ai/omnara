import { useCreateAssistantAgent } from '@omnara/react'
import type { AgentPanelSetup } from '@omnara/sdk'
import { useEffect, useRef } from 'react'

import { Button } from '@/components/ui/button'

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

export function AgentPanelBootstrap({
  setup,
  revision,
}: {
  setup: AgentPanelSetup
  revision: number
}) {
  const { mutate, isPending, error } = useCreateAssistantAgent()
  const attempted = useRef<string | null>(null)
  useEffect(() => {
    if (!setup.ready) return
    const key = `${setup.request_id}:${String(revision)}`
    if (attempted.current === key) return
    attempted.current = key
    mutate(automaticRequest(setup, revision))
  }, [mutate, revision, setup])

  return (
    <div className="text-muted-foreground space-y-2 p-4 text-sm">
      <p>
        {isPending
          ? 'Creating your assistant…'
          : setup.ready
            ? 'Your assistant is ready to set up.'
            : 'Your assistant will be created when this project has a model that supports tools.'}
      </p>
      {error != null && (
        <>
          <p role="alert" className="text-destructive">
            {error.message}
          </p>
          <Button
            size="sm"
            variant="outline"
            disabled={isPending}
            onClick={() => {
              mutate(automaticRequest(setup, revision))
            }}
          >
            Retry setup
          </Button>
        </>
      )}
    </div>
  )
}
