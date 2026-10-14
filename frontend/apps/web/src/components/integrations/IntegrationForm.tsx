import { Button } from '@/components/ui/button'
import { FieldGroup } from '@/components/ui/field'

import { IntegrationLauncherFields } from './IntegrationFormFields'
import {
  type IntegrationFormDraftOptions,
  useIntegrationFormDraft,
} from './useIntegrationFormDraft'

export interface IntegrationFormProps extends IntegrationFormDraftOptions {
  onCancel?: () => void
  cancelLabel?: string
}

export function IntegrationForm({
  orgId,
  projectId,
  integrationKind,
  integration,
  onSaved,
  onDiscard,
  onCancel,
  cancelLabel = 'Cancel',
  canEdit = true,
  defaultLauncherEnabled = false,
}: IntegrationFormProps) {
  const { values, dirty, stale, busy, error, validationError, canSave, change, discard, submit } =
    useIntegrationFormDraft({
      orgId,
      projectId,
      integrationKind,
      integration,
      onSaved,
      onDiscard,
      canEdit,
      defaultLauncherEnabled,
    })
  const missingProfile =
    integrationKind === 'github_pr' && values.launcher && values.profileIds.length === 0
  return (
    <form onSubmit={(event) => void submit(event)}>
      <FieldGroup>
        {stale && (
          <div role="alert" className="flex flex-col gap-2 text-sm">
            <p>
              Integration changed. Reload settings before saving. Reloading discards unsaved edits.
            </p>
            <Button type="button" variant="outline" disabled={busy} onClick={discard}>
              Reload settings
            </Button>
          </div>
        )}
        <div className="flex flex-col gap-6">
          <IntegrationLauncherFields
            orgId={orgId}
            projectId={projectId}
            integrationKind={integrationKind}
            integration={integration}
            values={values}
            onChange={change}
            disabled={!canEdit || busy || stale}
          />
        </div>
        {error && (
          <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
            {error}
          </p>
        )}
        {canEdit && !error && validationError && !missingProfile && (
          <p className="text-muted-foreground text-sm">{validationError}</p>
        )}
        {canEdit && (
          <IntegrationFormActions
            busy={busy}
            dirty={dirty}
            stale={stale}
            canSave={canSave}
            onCancel={onCancel}
            cancelLabel={cancelLabel}
            onDiscard={discard}
          />
        )}
      </FieldGroup>
    </form>
  )
}

function IntegrationFormActions({
  busy,
  dirty,
  stale,
  canSave,
  onCancel,
  cancelLabel,
  onDiscard,
}: {
  busy: boolean
  dirty: boolean
  stale: boolean
  canSave: boolean
  onCancel?: () => void
  cancelLabel: string
  onDiscard: () => void
}) {
  return (
    <div className="flex justify-end gap-2">
      {onCancel ? (
        <Button type="button" variant="outline" disabled={busy} onClick={onCancel}>
          {cancelLabel}
        </Button>
      ) : (
        dirty &&
        !stale && (
          <Button type="button" variant="outline" disabled={busy} onClick={onDiscard}>
            Discard changes
          </Button>
        )
      )}
      <Button type="submit" loading={busy} disabled={!canSave || busy}>
        Save changes
      </Button>
    </div>
  )
}
