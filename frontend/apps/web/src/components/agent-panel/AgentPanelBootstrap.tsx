import { useContext } from 'react'

import { Button } from '@/components/ui/button'

import { AssistantContext } from './assistant-context'

export function AgentPanelBootstrap() {
  const { bootstrap } = useContext(AssistantContext)
  if (bootstrap == null || !(bootstrap.pending || bootstrap.error != null)) return null
  return (
    <div className="text-muted-foreground space-y-2 p-4 text-sm">
      {bootstrap.pending && <p>Creating your assistant…</p>}
      <AssistantBootstrapError />
    </div>
  )
}

export function AssistantBootstrapError() {
  const { bootstrap } = useContext(AssistantContext)
  if (bootstrap?.error == null) return null
  return (
    <>
      <p role="alert" className="text-destructive text-sm">
        {bootstrap.error.message}
      </p>
      <Button size="sm" variant="outline" disabled={bootstrap.pending} onClick={bootstrap.retry}>
        Retry setup
      </Button>
    </>
  )
}
