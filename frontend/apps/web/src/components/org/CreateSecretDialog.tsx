import { useCreateSecret, useGrantSecretToProject, useStartSecretMcpOAuth } from '@omnara/react'
import type { SecretOwnerInput } from '@omnara/sdk'
import { type Dispatch, type SyntheticEvent, useReducer } from 'react'

import { isMcpOAuthLoginUrl } from '@/components/agents/mcpOAuthLogin'
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
import { resourceNameValid } from '@/lib/resource-name'

import { AWSCredentialsSecretFields } from './AWSCredentialsSecretFields'
import {
  isSecretKind,
  newSecretDialogState,
  type SecretDialogAction,
  secretDialogReducer,
  type SecretDialogState,
  secretKinds,
} from './CreateSecretDialogState'
import { submitSecretTransaction } from './createSecretSubmission'
import { McpOAuthSecretFields } from './McpOAuthSecretFields'
import { OAuthTokenFields } from './OAuthTokenFields'

type SecretDraft = SecretDialogState['secret']

function secretDraftValid(secret: SecretDraft): boolean {
  switch (secret.kind) {
    case 'generic':
      return secret.value !== ''
    case 'aws_credentials':
      return (
        secret.accessKeyId.trim() !== '' &&
        secret.secretAccessKey.trim() !== '' &&
        (secret.externalId.trim() === '' || secret.roleArn.trim() !== '')
      )
    case 'mcp_oauth':
      return isMcpOAuthLoginUrl(secret.serverUrl.trim())
    case 'oauth_token_set':
      return oauthTokenSetMaterial(secret.entries) !== undefined
  }
}

function submitLabel(state: SecretDialogState): string {
  if (state.createdSecret) return 'Retry project grants'
  return state.secret.kind === 'mcp_oauth' && secretDraftValid(state.secret)
    ? 'Authorize and Create Secret'
    : 'Create secret'
}

function SecretKindFields({
  secret,
  dispatch,
}: {
  secret: SecretDraft
  dispatch: Dispatch<SecretDialogAction>
}) {
  switch (secret.kind) {
    case 'generic':
      return (
        <Field>
          <FieldLabel htmlFor="secret-value">Value</FieldLabel>
          <Input
            id="secret-value"
            type="password"
            required
            value={secret.value}
            autoComplete="new-password"
            placeholder="sk-…"
            onChange={(event) => {
              dispatch({ type: 'set-generic-value', value: event.target.value })
            }}
          />
          <FieldDescription>Stored under the key value.</FieldDescription>
        </Field>
      )
    case 'aws_credentials':
      return (
        <AWSCredentialsSecretFields
          value={secret}
          onChange={(patch) => {
            dispatch({ type: 'patch-aws-credentials', patch })
          }}
        />
      )
    case 'mcp_oauth':
      return (
        <McpOAuthSecretFields
          value={secret}
          onChange={(patch) => {
            dispatch({ type: 'patch-mcp-oauth', patch })
          }}
        />
      )
    case 'oauth_token_set':
      return (
        <OAuthTokenFields
          entries={secret.entries}
          onChange={(entries) => {
            dispatch({ type: 'set-oauth-entries', entries })
          }}
        />
      )
  }
}

function SecretKindSelect({
  kind,
  dispatch,
}: {
  kind: SecretDraft['kind']
  dispatch: Dispatch<SecretDialogAction>
}) {
  return (
    <Field>
      <FieldLabel htmlFor="secret-kind">Kind</FieldLabel>
      <Select
        value={kind}
        onValueChange={(value) => {
          if (isSecretKind(value)) dispatch({ type: 'set-kind', kind: value })
        }}
      >
        <SelectTrigger id="secret-kind" className="w-full">
          <SelectValue>
            {secretKinds.find((option) => option.value === kind)?.label ?? kind}
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
  )
}

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
  const valid = resourceNameValid(state.name) && secretDraftValid(state.secret)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    dispatch({ type: 'submit-start' })
    const result = await submitSecretTransaction({
      state,
      owner,
      returnTo: window.location.pathname + window.location.search + window.location.hash,
      operations: {
        createSecret: createSecret.mutateAsync,
        grantSecret: async (input) => {
          await grantSecret.mutateAsync(input)
        },
        startMcpOAuth: startMcpOAuth.mutateAsync,
        savePendingMcpGrants: (projectIds) => {
          savePendingMcpSecretGrants(orgId, projectIds)
        },
      },
    })

    if ('secret' in result && result.secret && !state.createdSecret) {
      dispatch({ type: 'created', secret: result.secret })
    }
    settleSubmission(result)
  }

  function settleSubmission(result: Awaited<ReturnType<typeof submitSecretTransaction>>) {
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
                value={state.name}
                autoComplete="off"
                placeholder="openai-prod"
                onChange={(event) => {
                  dispatch({ type: 'set-name', name: event.target.value })
                }}
              />
              <ResourceNameFieldError value={state.name} />
            </Field>
            <SecretKindSelect kind={state.secret.kind} dispatch={dispatch} />
            <SecretKindFields secret={state.secret} dispatch={dispatch} />
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
                {submitLabel(state)}
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
