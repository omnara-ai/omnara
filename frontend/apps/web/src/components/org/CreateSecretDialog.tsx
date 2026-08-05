import { useCreateSecret, useGrantSecretToProject, useStartSecretMcpOAuth } from '@omnara/react'
import type { SecretOwnerInput } from '@omnara/sdk'
import { type SyntheticEvent, useReducer } from 'react'

import { ProjectGrantsField } from '@/components/projects/ProjectGrantsField'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { collectGrantFailures } from '@/lib/grant-failures'
import { oauthTokenSetMaterial } from '@/lib/oauthEntries'
import { savePendingMcpSecretGrants } from '@/lib/pending-mcp-secret-grants'
import { errorMessage } from '@/lib/submit-status'

import {
  isSecretKind,
  newSecretDialogState,
  secretDialogReducer,
  secretKinds,
} from './CreateSecretDialogState'
import { McpOAuthSecretFields } from './McpOAuthSecretFields'
import { OAuthTokenFields } from './OAuthTokenFields'

export function CreateSecretDialog({
  open,
  onOpenChange,
  orgId,
  owner,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  owner: SecretOwnerInput
}) {
  const createSecret = useCreateSecret(orgId)
  const grantSecret = useGrantSecretToProject(orgId)
  const startMcpOAuth = useStartSecretMcpOAuth(orgId)
  const [state, dispatch] = useReducer(secretDialogReducer, undefined, newSecretDialogState)

  const validMcpUrl =
    state.secret.kind === 'mcp_oauth' && isValidMcpUrl(state.secret.serverUrl.trim())
  const oauthMaterial =
    state.secret.kind === 'oauth_token_set'
      ? oauthTokenSetMaterial(state.secret.entries)
      : undefined
  const valid =
    state.name.trim() !== '' &&
    (state.secret.kind === 'generic'
      ? state.secret.value !== ''
      : state.secret.kind === 'mcp_oauth'
        ? validMcpUrl
        : oauthMaterial !== undefined)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    dispatch({ type: 'submit-start' })
    let navigating = false
    try {
      if (state.secret.kind === 'mcp_oauth' && !state.createdSecret) {
        const trimmedMcpServerUrl = state.secret.serverUrl.trim()
        const trimmedClientId = state.secret.clientId.trim()
        const trimmedClientSecret = state.secret.clientSecret?.trim() ?? ''
        const response = await startMcpOAuth.mutateAsync({
          owner,
          name: state.name,
          mcp_url: trimmedMcpServerUrl,
          return_to: window.location.pathname + window.location.search + window.location.hash,
          ...(trimmedClientId !== '' ? { client_id: trimmedClientId } : {}),
          ...(trimmedClientSecret !== '' ? { client_secret: trimmedClientSecret } : {}),
        })
        savePendingMcpSecretGrants(orgId, state.projectGrantIds)
        navigating = true
        window.location.assign(response.authorization_url)
        return
      }

      let secret = state.createdSecret
      if (!secret) {
        if (state.secret.kind === 'mcp_oauth') return
        const material =
          state.secret.kind === 'generic'
            ? ({ kind: 'generic', value: state.secret.value } as const)
            : oauthTokenSetMaterial(state.secret.entries)
        if (material === undefined) {
          throw new Error('OAuth token material is incomplete')
        }
        secret = await createSecret.mutateAsync({
          owner,
          name: state.name,
          material,
        })
        dispatch({ type: 'created', secret })
      }
      const secretID = secret.id
      const grantResults = await Promise.allSettled(
        state.projectGrantIds.map((targetProjectID) =>
          grantSecret.mutateAsync({ secretID, projectID: targetProjectID }),
        ),
      )
      const failures = collectGrantFailures(state.projectGrantIds, grantResults)
      if (failures) {
        dispatch({
          type: 'grant-failures',
          failedProjectIds: failures.failedProjectIds,
          message: `The secret was created, but ${failures.message}`,
        })
        return
      }
      dispatch({ type: 'reset' })
      onOpenChange(false)
    } catch (err) {
      dispatch({ type: 'submit-fail', message: errorMessage(err, 'Could not create secret') })
    } finally {
      if (!navigating) {
        dispatch({ type: 'submit-settled' })
      }
    }
  }

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) {
      dispatch({ type: 'closed' })
    }
    onOpenChange(nextOpen)
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New secret</DialogTitle>
          <DialogDescription>
            Store API keys and credentials your providers and pools use.
          </DialogDescription>
        </DialogHeader>
        <form
          autoComplete="off"
          onSubmit={(event) => {
            void submit(event)
          }}
        >
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="secret-name">Name</FieldLabel>
              <Input
                id="secret-name"
                required
                value={state.name}
                autoComplete="off"
                placeholder="openai-prod"
                onChange={(event) => {
                  dispatch({ type: 'set-name', name: event.target.value })
                }}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="secret-kind">Kind</FieldLabel>
              <Select
                value={state.secret.kind}
                onValueChange={(value) => {
                  if (isSecretKind(value)) dispatch({ type: 'set-kind', kind: value })
                }}
              >
                <SelectTrigger id="secret-kind" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {secretKinds.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {option.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            {state.secret.kind === 'generic' ? (
              <Field>
                <FieldLabel htmlFor="secret-value">Value</FieldLabel>
                <Input
                  id="secret-value"
                  type="password"
                  required
                  value={state.secret.value}
                  autoComplete="new-password"
                  placeholder="sk-…"
                  onChange={(event) => {
                    dispatch({ type: 'set-generic-value', value: event.target.value })
                  }}
                />
                <FieldDescription>Stored under the key value.</FieldDescription>
              </Field>
            ) : state.secret.kind === 'mcp_oauth' ? (
              <McpOAuthSecretFields
                value={state.secret}
                onChange={(patch) => {
                  dispatch({ type: 'patch-mcp-oauth', patch })
                }}
              />
            ) : (
              <OAuthTokenFields
                entries={state.secret.entries}
                onChange={(entries) => {
                  dispatch({ type: 'set-oauth-entries', entries })
                }}
              />
            )}
            <ProjectGrantsField
              orgId={orgId}
              isProjectEligible={(project) => project.access.can_manage}
              excludedProjectIds={owner.kind === 'project' ? [owner.project_id] : []}
              value={state.projectGrantIds}
              onChange={(ids) => {
                dispatch({ type: 'set-project-grant-ids', ids })
              }}
              disabled={state.submitting}
            />
            {state.error && <p className="text-destructive text-sm">{state.error}</p>}
            <DialogFooter>
              <Button type="submit" disabled={state.submitting || (!state.createdSecret && !valid)}>
                {state.submitting && <Spinner />}
                {state.createdSecret
                  ? 'Retry project grants'
                  : state.secret.kind === 'mcp_oauth' && validMcpUrl
                    ? 'Authorize and Create Secret'
                    : 'Create secret'}
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function isValidMcpUrl(value: string) {
  try {
    const url = new URL(value)
    return url.protocol === 'https:'
  } catch {
    return false
  }
}
