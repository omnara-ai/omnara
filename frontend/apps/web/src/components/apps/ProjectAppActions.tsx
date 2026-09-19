import { useDeleteProjectApp, useUpdateProjectApp } from '@omnara/react'
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
  const update = useUpdateProjectApp(orgId, projectId)
  const remove = useDeleteProjectApp(orgId, projectId)
  const [error, setError] = useState('')
  const busy = update.isPending || remove.isPending
  return (
    <div className="flex flex-col items-start gap-2">
      <div className="flex flex-wrap gap-2">
        {onEdit && (
          <Button size="sm" variant="outline" disabled={busy} onClick={onEdit}>
            Edit settings
          </Button>
        )}
        <Button
          size="sm"
          variant="outline"
          disabled={busy}
          onClick={() => {
            setError('')
            update.mutate(
              { appID: app.id, name: app.name, enabled: !app.enabled, settings: app.settings },
              {
                onError: (cause) => {
                  setError(errorMessage(cause, 'Could not update app.'))
                },
              },
            )
          }}
        >
          {app.enabled ? 'Disable app' : 'Enable app'}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          disabled={busy}
          onClick={() => {
            if (
              !window.confirm(
                `Remove app ${app.name}? This stops its launcher. Existing agents and the provider connection are kept.`,
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
