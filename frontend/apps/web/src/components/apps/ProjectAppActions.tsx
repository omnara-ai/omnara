import { useDeleteProjectApp, useDisconnectProjectApp } from '@omnara/react'
import type { ProjectApp } from '@omnara/sdk'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { errorMessage } from '@/lib/submit-status'

export function ProjectAppActions({
  orgId,
  projectId,
  app,
  onEdit,
  onRemoved,
}: {
  orgId: string
  projectId: string
  app: ProjectApp
  onEdit?: () => void
  onRemoved: () => void
}) {
  const disconnect = useDisconnectProjectApp(orgId, projectId)
  const remove = useDeleteProjectApp(orgId, projectId)
  const [error, setError] = useState('')
  const busy = disconnect.isPending || remove.isPending
  return (
    <div className="flex flex-col items-start gap-2">
      <div className="flex flex-wrap gap-2">
        {onEdit && (
          <Button size="sm" variant="outline" disabled={busy} onClick={onEdit}>
            Edit settings
          </Button>
        )}
        {app.state === 'active' && (
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={() => {
              setError('')
              if (
                !window.confirm(
                  `Disconnect ${app.name}? Its tools, listeners and launcher will lose provider access. Agents and history are kept.`,
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
          size="sm"
          variant="ghost"
          disabled={busy}
          onClick={() => {
            if (
              !window.confirm(
                `Remove app ${app.name}? This deletes its schedules and revokes its tools, listeners and launcher. Existing agents and history are kept.`,
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
    </div>
  )
}
