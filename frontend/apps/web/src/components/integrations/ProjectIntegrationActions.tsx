import type { ProjectIntegration } from '@omnara/sdk'
import { useState } from 'react'

import { MoreHorizontalIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { errorMessage } from '@/lib/submit-status'

import type { useProjectIntegrationActions } from './useProjectIntegrationActions'

export function ProjectIntegrationActions({
  actions,
  integration,
  onConnect,
}: {
  actions: ReturnType<typeof useProjectIntegrationActions>
  integration: ProjectIntegration
  onConnect?: () => void
}) {
  const { disconnect, busy } = actions
  const [error, setError] = useState('')
  return (
    <div className="flex flex-col items-end gap-2">
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" disabled={busy} aria-label="Integration actions">
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {onConnect && (
            <DropdownMenuItem disabled={busy} onSelect={onConnect}>
              Reconnect account
            </DropdownMenuItem>
          )}
          {integration.state === 'active' && (
            <DropdownMenuItem
              disabled={busy}
              onSelect={() => {
                setError('')
                if (
                  !window.confirm(
                    `Disconnect ${integration.name}? Provider access and conversation forwarding will pause. Subscriptions, agents and history are kept.`,
                  )
                )
                  return
                disconnect.mutate(integration.id, {
                  onError: (cause) => {
                    setError(errorMessage(cause, 'Could not disconnect integration.'))
                  },
                })
              }}
            >
              Disconnect integration
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      {error && (
        <p role="alert" className="text-destructive max-w-xs text-sm">
          {error}
        </p>
      )}
    </div>
  )
}

export function RemoveProjectIntegrationButton({
  actions,
  integration,
  onRemoved,
}: {
  actions: ReturnType<typeof useProjectIntegrationActions>
  integration: ProjectIntegration
  onRemoved: () => void
}) {
  const { remove, busy } = actions
  const [error, setError] = useState('')
  return (
    <div className="flex flex-col items-start gap-2">
      <Button
        type="button"
        variant="outline"
        className="hover:text-destructive"
        disabled={busy}
        loading={remove.isPending}
        onClick={() => {
          if (
            !window.confirm(
              `Delete integration ${integration.name}? This deletes its schedules and subscriptions and revokes its tools and launcher. Existing agents and history are kept.`,
            )
          )
            return
          setError('')
          remove.mutate(integration.id, {
            onSuccess: onRemoved,
            onError: (cause) => {
              setError(errorMessage(cause, 'Could not delete integration.'))
            },
          })
        }}
      >
        Delete integration
      </Button>
      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
    </div>
  )
}
