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
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { oauthTokenSetMaterial } from '@/lib/oauthEntries'
import { savePendingMcpSecretGrants } from '@/lib/pending-mcp-secret-grants'
import { resourceNameInputMaxLength, resourceNameValid } from '@/lib/resource-name'

import { AWSCredentialsSecretFields } from './AWSCredentialsSecretFields'
import {
  isSecretKind,
  newSecretDialogState,
  secretDialogReducer,
  secretKinds,
} from './CreateSecretDialogState'
import { submitSecretTransaction } from './createSecretSubmission'
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
    resourceNameValid(state.name) &&
    (state.secret.kind === 'generic'
      ? state.secret.value !== ''
      : state.secret.kind === 'aws_credentials'
        ? state.secret.accessKeyId.trim() !== '' &&
          state.secret.secretAccessKey.trim() !== '' &&
          (state.secret.externalId.trim() === '' || state.secret.roleArn.trim() !== '')
        : state.secret.kind === 'mcp_oauth'
          ? validMcpUrl
          : oauthMaterial !== undefined)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    dispatch({ type: 'submit-start' })
    const result = await submitSecretTransaction({
      state,
      owner,
      returnTo: window.location.pathname + window.location.search + window.location.hash,
      operations: {
        createSecret: createSecret.mutateAsync,
        grantSecret: grantSecret.mutateAsync,
        startMcpOAuth: startMcpOAuth.mutateAsync,
        savePendingMcpGrants: (projectIds) => {
          savePendingMcpSecretGrants(orgId, projectIds)
        },
      },
    })

    if ('secret' in result && result.secret && !state.createdSecret) {
      dispatch({ type: 'created', secret: result.secret })
    }
    switch (result.kind) {
      case 'redirect':
        window.location.assign(result.authorizationUrl)
        return
      case 'failed':
        dispatch({ type: 'submit-fail', message: result.message })
        return
      case 'grant-failures':
        dispatch({
          type: 'grant-failures',
          failedProjectIds: result.failedProjectIds,
          message: result.message,
        })
        dispatch({ type: 'submit-settled' })
        return
      case 'complete':
        dispatch({ type: 'reset' })
        onOpenChange(false)
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
                maxLength={resourceNameInputMaxLength}
                value={state.name}
                autoComplete="off"
                placeholder="openai-prod"
                onChange={(event) => {
                  dispatch({ type: 'set-name', name: event.target.value })
                }}
              />
              <ResourceNameFieldError value={state.name} />
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
                  <SelectValue>
                    {secretKinds.find((option) => option.value === state.secret.kind)?.label ??
                      state.secret.kind}
                  </SelectValue>
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
            ) : state.secret.kind === 'aws_credentials' ? (
              <AWSCredentialsSecretFields
                value={state.secret}
                onChange={(patch) => {
                  dispatch({ type: 'patch-aws-credentials', patch })
                }}
              />
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
              <Button
                type="submit"
                disabled={state.submitting || (!state.createdSecret && !valid)}
                loading={state.submitting}
              >
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
