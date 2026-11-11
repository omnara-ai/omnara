import type { AgentPanelSelection } from '@omnara/sdk'
import { useNavigate } from '@tanstack/react-router'
import { useContext } from 'react'

import { ArrowUpRight, SettingsIcon, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { ActiveOrgContext } from '@/lib/active-org-context'

export function AgentPanelHeader({
  selection,
  settings,
  onSettings,
  onClose,
}: {
  selection: AgentPanelSelection | undefined
  settings: boolean
  onSettings: () => void
  onClose: () => void
}) {
  const activeOrg = useContext(ActiveOrgContext)
  const navigate = useNavigate()
  return (
    <header className="flex shrink-0 items-center gap-2 border-b px-4 py-3">
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium">
          {selection == null || selection.name === '' ? 'Your assistant' : selection.name}
        </p>
        {selection != null && (
          <p className="text-muted-foreground truncate text-xs">
            {selection.scope.org_name} / {selection.scope.project_name}
          </p>
        )}
      </div>
      {selection != null && (
        <Button
          variant="ghost"
          size="icon"
          aria-label="Open agent"
          onClick={() => {
            activeOrg?.setActiveOrgId(selection.scope.org_id)
            onClose()
            void navigate({
              to: '/projects/$projectId/agents/$agentId/chat',
              params: { projectId: selection.scope.project_id, agentId: selection.agent_id },
            })
          }}
        >
          <ArrowUpRight className="size-4" />
        </Button>
      )}
      <Button
        variant="ghost"
        size="icon"
        aria-label="Assistant settings"
        aria-pressed={settings}
        onClick={onSettings}
      >
        <SettingsIcon className="size-4" />
      </Button>
      <Button variant="ghost" size="icon" aria-label="Close assistant" onClick={onClose}>
        <X className="size-4" />
      </Button>
    </header>
  )
}
