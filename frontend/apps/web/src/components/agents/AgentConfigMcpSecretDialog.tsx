import { useCreateSecret } from '@omnara/react'
import type { Secret, SecretOwnerInput } from '@omnara/sdk'
import { useState } from 'react'

import {
  defaultMcpSecretName,
  isMcpOAuthLoginUrl,
  useMcpOAuthLogin,
} from '@/components/agents/mcpOAuthLogin'
import { PillTabs } from '@/components/agents/PillTabs'
import type { BasicMcpServer } from '@/components/agents/useAgentBuilderForm'
import { AWSCredentialsSecretFields } from '@/components/org/AWSCredentialsSecretFields'
import type { AWSCredentialsSecretFormSecret } from '@/components/org/CreateSecretDialogState'
import { McpOAuthClientFields } from '@/components/org/McpOAuthClientFields'
import { OAuthTokenFields } from '@/components/org/OAuthTokenFields'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, RequiredFieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { newOAuthTokenSetEntries, type OAuthEntry, oauthTokenSetMaterial } from '@/lib/oauthEntries'
import { errorMessage } from '@/lib/submit-status'

type OAuthMethod = 'login' | 'tokens'

const emptyAws: AWSCredentialsSecretFormSecret = {
  kind: 'aws_credentials',
  accessKeyId: '',
  secretAccessKey: '',
  sessionToken: '',
  roleArn: '',
  externalId: '',
}

function optionalField(value: string) {
  const trimmed = value.trim()
  return trimmed === '' ? undefined : trimmed
}

function awsMaterial(aws: AWSCredentialsSecretFormSecret) {
  const accessKeyId = aws.accessKeyId.trim()
  const secretAccessKey = aws.secretAccessKey.trim()
  const roleArn = optionalField(aws.roleArn)
  const externalId = optionalField(aws.externalId)
  if (accessKeyId === '' || secretAccessKey === '') return undefined
  if (externalId !== undefined && roleArn === undefined) return undefined
  const sessionToken = optionalField(aws.sessionToken)
  return {
    kind: 'aws_credentials',
    access_key_id: accessKeyId,
    secret_access_key: secretAccessKey,
    ...(sessionToken === undefined ? {} : { session_token: sessionToken }),
    ...(roleArn === undefined ? {} : { role_arn: roleArn }),
    ...(externalId === undefined ? {} : { external_id: externalId }),
  } as const
}

export function AgentConfigMcpSecretDialog({
  orgId,
  projectId,
  server,
  initialError = '',
  onClose,
  onCreated,
  onBeforeOAuthRedirect,
}: {
  orgId: string
  projectId: string
  server: BasicMcpServer
  initialError?: string
  onClose: () => void
  onCreated: (secret: Secret) => void
  onBeforeOAuthRedirect: () => void
}) {
  const createSecret = useCreateSecret(orgId)
  const login = useMcpOAuthLogin({
    orgId,
    projectId,
    server,
    onBeforeRedirect: onBeforeOAuthRedirect,
  })
  const oauth = server.authType === 'oauth'
  const sigv4 = server.authType === 'sigv4'
  const [name, setName] = useState(() => (oauth ? defaultMcpSecretName(server) : ''))
  const [bearerValue, setBearerValue] = useState('')
  const [aws, setAws] = useState(emptyAws)
  const [method, setMethod] = useState<OAuthMethod>('login')
  const [clientId, setClientId] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [entries, setEntries] = useState<OAuthEntry[]>(newOAuthTokenSetEntries)
  const [error, setError] = useState(initialError)

  const owner: SecretOwnerInput = { kind: 'project', project_id: projectId }
  const idPrefix = `${server.id}-new-secret`
  const mcpUrl = server.url.trim()
  const loginMode = oauth && method === 'login'
  const tokenMaterial = oauthTokenSetMaterial(entries)
  const sigv4Material = awsMaterial(aws)
  const submitting = createSecret.isPending || login.pending
  const valid =
    name.trim() !== '' &&
    (oauth
      ? isMcpOAuthLoginUrl(mcpUrl) && (loginMode || tokenMaterial !== undefined)
      : sigv4
        ? sigv4Material !== undefined
        : bearerValue !== '')

  async function submit() {
    setError('')
    try {
      if (loginMode) {
        await login.start({ name, clientId, clientSecret })
        return
      }
      const secret = oauth
        ? tokenMaterial && {
            metadata: { mcp_url: mcpUrl },
            material: { ...tokenMaterial, mcp_url: mcpUrl },
          }
        : sigv4
          ? sigv4Material && { material: sigv4Material }
          : { material: { kind: 'generic', value: bearerValue } as const }
      if (secret === undefined) return
      onCreated(await createSecret.mutateAsync({ owner, name: name.trim(), ...secret }))
    } catch (err) {
      setError(errorMessage(err, 'Could not create secret'))
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !submitting) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {oauth ? 'Add OAuth secret' : sigv4 ? 'Add AWS credentials' : 'Add bearer secret'}
          </DialogTitle>
          <DialogDescription>
            {oauth
              ? `Stored as a project secret bound to ${mcpUrl || 'this server URL'}.`
              : 'Stored as a project secret.'}
          </DialogDescription>
        </DialogHeader>
        <form
          autoComplete="off"
          onSubmit={(event) => {
            event.preventDefault()
            if (valid && !submitting) void submit()
          }}
        >
          <FieldGroup>
            {oauth && (
              <PillTabs
                value={method}
                onValueChange={setMethod}
                tabs={[
                  { value: 'login', label: 'Login' },
                  { value: 'tokens', label: 'Enter tokens' },
                ]}
              />
            )}
            <Field>
              <RequiredFieldLabel htmlFor={`${idPrefix}-name`}>Secret name</RequiredFieldLabel>
              <Input
                id={`${idPrefix}-name`}
                value={name}
                autoComplete="off"
                placeholder={sigv4 ? 'aws-credentials' : 'mcp-token'}
                onChange={(event) => {
                  setName(event.target.value)
                }}
              />
            </Field>
            {sigv4 ? (
              <AWSCredentialsSecretFields
                value={aws}
                onChange={(patch) => {
                  setAws((current) => ({ ...current, ...patch }))
                }}
              />
            ) : !oauth ? (
              <Field>
                <RequiredFieldLabel htmlFor={`${idPrefix}-value`}>Bearer token</RequiredFieldLabel>
                <Input
                  id={`${idPrefix}-value`}
                  type="password"
                  value={bearerValue}
                  autoComplete="new-password"
                  onChange={(event) => {
                    setBearerValue(event.target.value)
                  }}
                />
              </Field>
            ) : loginMode ? (
              <McpOAuthClientFields
                idPrefix={idPrefix}
                clientId={clientId}
                clientSecret={clientSecret}
                onChange={(patch) => {
                  if (patch.clientId !== undefined) setClientId(patch.clientId)
                  if (patch.clientSecret !== undefined) setClientSecret(patch.clientSecret)
                }}
              />
            ) : (
              <OAuthTokenFields entries={entries} onChange={setEntries} hiddenKeys={['mcp_url']} />
            )}
            {loginMode && !isMcpOAuthLoginUrl(mcpUrl) && (
              <FieldDescription>Enter an https:// server URL to log in.</FieldDescription>
            )}
            {error && <p className="text-destructive text-sm">{error}</p>}
            <DialogFooter>
              <Button type="submit" disabled={submitting || !valid} loading={submitting}>
                {loginMode ? `Login to ${mcpUrl || 'MCP server'}` : 'Create secret'}
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
