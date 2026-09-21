import { useDeleteProjectApp, useDisconnectProjectApp } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'
import { useState } from 'react'

import { MoreHorizontalIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { errorMessage } from '@/lib/submit-status'

export function ProjectAppActions({
  orgId,
  projectId,
  app,
  onConnect,
  onRemoved,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  onConnect?: () => void
  onRemoved: () => void
}) {
  const disconnect = useDisconnectProjectApp(orgId, projectId)
  const remove = useDeleteProjectApp(orgId, projectId)
  const [error, setError] = useState('')
  const busy = disconnect.isPending || remove.isPending
  return (
    <div className="flex flex-col items-end gap-2">
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" disabled={busy} aria-label="App actions">
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {onConnect && (
            <DropdownMenuItem disabled={busy} onSelect={onConnect}>
              Reconnect account
            </DropdownMenuItem>
          )}
          {app.state === 'active' && (
            <DropdownMenuItem
              disabled={busy}
              onSelect={() => {
                setError('')
                if (
                  !window.confirm(
                    `Disconnect ${app.name}? Provider access and conversation forwarding will pause. Subscriptions, agents and history are kept.`,
                  )
                )
                  return
                disconnect.mutate(app.id, {
                  onError: (cause) => {
                    setError(errorMessage(cause, 'Could not disconnect app.'))
                  },
                })
              }}
            >
              Disconnect app
            </DropdownMenuItem>
          )}
          {(onConnect !== undefined || app.state === 'active') && <DropdownMenuSeparator />}
          <DropdownMenuItem
            variant="destructive"
            disabled={busy}
            onSelect={() => {
              if (
                !window.confirm(
                  `Remove app ${app.name}? This deletes its schedules and subscriptions and revokes its tools and launcher. Existing agents and history are kept.`,
                )
              )
                return
              setError('')
              remove.mutate(app.id, {
                onSuccess: onRemoved,
                onError: (cause) => {
                  setError(errorMessage(cause, 'Could not remove app.'))
                },
              })
            }}
          >
            Remove app
          </DropdownMenuItem>
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
