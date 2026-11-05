import { useAgentPanel, useUpdateAgentPanel } from '@omnara/react'

import { Button } from '@/components/ui/button'

export function AgentPanelSetting() {
  const { data: panel } = useAgentPanel()
  const update = useUpdateAgentPanel()
  return (
    <section className="space-y-2">
      <h2 className="text-sm font-medium">Assistant panel</h2>
      <p className="text-muted-foreground text-sm">
        Keep your chosen agent available across organizations. Turning the panel off keeps the agent
        and its history.
      </p>
      <Button
        variant="outline"
        disabled={panel == null || update.isPending}
        onClick={() => {
          update.mutate({ enabled: !panel?.enabled })
        }}
      >
        {panel?.enabled === false ? 'Turn on assistant panel' : 'Turn off assistant panel'}
      </Button>
      {update.error != null && (
        <p role="alert" className="text-destructive text-sm">
          {update.error.message}
        </p>
      )}
    </section>
  )
}
