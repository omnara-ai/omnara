import { useUpdateIntegration } from '@omnara/react'
import type { Integration, IntegrationKind } from '@omnara/sdk'
import { type SyntheticEvent, useEffect, useRef, useState } from 'react'

import { errorMessage } from '@/lib/submit-status'

import {
  integrationFormRequest,
  type IntegrationFormValues,
  integrationFormValues,
  validateIntegrationForm,
} from './integrationFormState'

export interface IntegrationFormDraftOptions {
  orgId: string
  projectId: string
  integrationKind: IntegrationKind
  integration: Integration
  onSaved: (integration: Integration) => void
  onDiscard?: () => void
  canEdit?: boolean
  defaultLauncherEnabled?: boolean
}

interface Draft {
  source: Integration
  base: Integration
  values: IntegrationFormValues
  stale: boolean
}

function resetDraft(draft: Draft, kind: IntegrationKind, base: Integration): Draft {
  return { ...draft, base, values: integrationFormValues(kind, base), stale: false }
}

function observeIntegration(
  draft: Draft,
  integration: Integration,
  kind: IntegrationKind,
  busy: boolean,
  dirty: boolean,
): Draft {
  const observed = { ...draft, source: integration }
  if (integration.id !== draft.base.id) return resetDraft(observed, kind, integration)
  // A save writes its own cache entry before its refetches finish. Observe every
  // source during that interval, but resolve conflicts only when the save settles.
  if (busy || integration.updated_at === draft.source.updated_at) return observed
  if (!dirty) return resetDraft(observed, kind, integration)
  return { ...observed, stale: integration.updated_at !== draft.base.updated_at }
}

function hasObservedChange(draft: Draft, submitted: Draft) {
  // Versions are opaque strings: parsing timestamps would discard Go's sub-ms precision.
  // A delayed prop catching up to the submitted base is not a new external change.
  return (
    draft.source.updated_at !== submitted.source.updated_at &&
    draft.source.updated_at !== submitted.base.updated_at
  )
}

function acceptSave(draft: Draft, submitted: Draft, saved: Integration, kind: IntegrationKind) {
  return resetDraft(draft, kind, hasObservedChange(draft, submitted) ? draft.source : saved)
}

export function useIntegrationFormDraft({
  orgId,
  projectId,
  integrationKind,
  integration,
  onSaved,
  onDiscard,
  canEdit = true,
  defaultLauncherEnabled = false,
}: IntegrationFormDraftOptions) {
  const update = useUpdateIntegration(orgId, projectId)
  const [editor, setEditor] = useState<Draft>(() => ({
    source: integration,
    base: integration,
    values: integrationFormValues(integrationKind, integration, defaultLauncherEnabled),
    stale: false,
  }))
  const [error, setError] = useState('')
  const submitting = useRef(false)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const { base, values, stale } = editor
  const dirty =
    JSON.stringify(values) !== JSON.stringify(integrationFormValues(integrationKind, base))
  const busy = update.isPending
  if (integration !== editor.source) {
    setEditor(observeIntegration(editor, integration, integrationKind, busy, dirty))
  }
  const validationError = validateIntegrationForm(integrationKind, values, base)
  const canSave = canEdit && dirty && !stale && !validationError

  function change(patch: Partial<IntegrationFormValues>) {
    setEditor((previous) => ({ ...previous, values: { ...previous.values, ...patch } }))
  }
  function discard() {
    setEditor(resetDraft(editor, integrationKind, stale ? editor.source : base))
    setError('')
    onDiscard?.()
  }
  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!canSave || submitting.current) return
    submitting.current = true
    setError('')
    try {
      const request = integrationFormRequest(integrationKind, values, base)
      const saved = await update.mutateAsync({ integrationID: base.id, ...request })
      if (mounted.current) {
        setEditor((previous) => acceptSave(previous, editor, saved, integrationKind))
        onSaved(saved)
      }
    } catch (cause) {
      if (mounted.current) {
        setError(errorMessage(cause, 'Could not save integration.'))
        setEditor((previous) => ({
          ...previous,
          stale: hasObservedChange(previous, editor),
        }))
      }
    } finally {
      submitting.current = false
    }
  }
  return { values, dirty, stale, busy, error, validationError, canSave, change, discard, submit }
}
