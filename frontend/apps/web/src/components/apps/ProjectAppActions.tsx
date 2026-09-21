import type { ProjectApp } from '@omnara/sdk'
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

import type { useProjectAppActions } from './useProjectAppActions'

export function ProjectAppActions({
  actions,
  app,
  onConnect,
}: {
  actions: ReturnType<typeof useProjectAppActions>
  app: ProjectApp
  onConnect?: () => void
}) {
  const { disconnect, busy } = actions
  const [error, setError] = useState('')
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

export function RemoveProjectAppButton({
  actions,
  app,
  onRemoved,
}: {
  actions: ReturnType<typeof useProjectAppActions>
  app: ProjectApp
  onRemoved: () => void
}) {
  const { remove, busy } = actions
  const [error, setError] = useState('')
  return (
    <div className="flex flex-col items-start gap-2">
      <Button
        variant="ghost"
        size="sm"
        className="text-muted-foreground hover:text-destructive h-auto self-start p-0 font-normal hover:bg-transparent"
        disabled={busy}
        loading={remove.isPending}
        onClick={() => {
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
      </Button>
      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
    </div>
  )
}
