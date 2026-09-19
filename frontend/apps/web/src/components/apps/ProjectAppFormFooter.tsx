import type { IntegrationConnection } from '@omnara/sdk'

import { Button } from '@/components/ui/button'

export function ProjectAppFormFooter({
  busy,
  blocked,
  editing,
  savedConnection,
  error,
  validationError,
  missingSavedKey,
  onCancel,
}: {
  busy: boolean
  blocked: boolean
  editing: boolean
  savedConnection?: IntegrationConnection
  error: string
  validationError: string
  missingSavedKey: boolean
  onCancel?: () => void
}) {
  return (
    <>
      {savedConnection && (
        <p className="text-sm">
          Connection saved: {savedConnection.id}. Retry creates only the app. Leaving this page
          keeps the connection.
        </p>
      )}
      {missingSavedKey && (
        <p role="alert">
          Save a valid public_key on this Discord connection before enabling multiple choices or
          agent questions.
        </p>
      )}
      {error ? (
        <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
          {error}
        </p>
      ) : (
        validationError && <p className="text-muted-foreground text-sm">{validationError}</p>
      )}
      <div className="flex justify-end gap-2">
        {onCancel && (
          <Button type="button" variant="outline" disabled={busy} onClick={onCancel}>
            Cancel
          </Button>
        )}
        <Button type="submit" loading={busy} disabled={busy || blocked}>
          {editing ? 'Save changes' : 'Create app'}
        </Button>
      </div>
    </>
  )
}
