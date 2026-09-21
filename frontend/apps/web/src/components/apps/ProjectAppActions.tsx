import { useDeleteProjectApp, useDisconnectProjectApp } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
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
    <section aria-label="App actions" className="flex flex-col gap-3 border-t pt-6">
      <div className="flex flex-wrap items-center gap-3">
        {onConnect && (
          <Button variant="outline" disabled={busy} onClick={onConnect}>
            Reconnect account
          </Button>
        )}
        {app.state === 'active' && (
          <Button
            variant="ghost"
            disabled={busy}
            loading={disconnect.isPending}
            onClick={() => {
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
          </Button>
        )}
        <Button
          variant="ghost"
          className="text-destructive hover:text-destructive sm:ml-auto"
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
      </div>
      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
    </section>
  )
}
