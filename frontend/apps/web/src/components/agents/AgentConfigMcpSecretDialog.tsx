import { useCreateSecret } from '@omnara/react'
import type { Secret, SecretMaterial, SecretOwnerInput } from '@omnara/sdk'
import { useState } from 'react'

import {
  defaultMcpSecretName,
  isMcpOAuthLoginUrl,
  useMcpOAuthLogin,
} from '@/components/agents/mcpOAuthLogin'
import { PillTabs } from '@/components/agents/PillTabs'
import type { BasicMcpServer } from '@/components/agents/useAgentBuilderForm'
import { AWSCredentialsSecretFields } from '@/components/org/AWSCredentialsSecretFields'
import {
  awsCredentialsMaterial,
  newAWSCredentialsSecret,
} from '@/components/org/CreateSecretDialogState'
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

const dialogCopy: Record<BasicMcpServer['authType'], { title: string; placeholder: string }> = {
  none: { title: 'Add bearer secret', placeholder: 'mcp-token' },
  bearer: { title: 'Add bearer secret', placeholder: 'mcp-token' },
  oauth: { title: 'Add OAuth secret', placeholder: 'mcp-token' },
  sigv4: { title: 'Add AWS credentials', placeholder: 'aws-credentials' },
}

function dialogDescription(authType: BasicMcpServer['authType'], mcpUrl: string) {
  if (authType === 'oauth') {
    return `Stored as a project secret bound to ${mcpUrl || 'this server URL'}.`
  }
  return 'Stored as a project secret.'
}

function submitLabel(loginMode: boolean, mcpUrl: string) {
  if (loginMode) return `Login to ${mcpUrl || 'MCP server'}`
  return 'Create secret'
}

function authMaterialValid(
  authType: BasicMcpServer['authType'],
  mcpUrl: string,
  loginMode: boolean,
  tokenMaterial: ReturnType<typeof oauthTokenSetMaterial>,
  sigv4Material: ReturnType<typeof awsCredentialsMaterial>,
  bearerValue: string,
) {
  if (authType === 'oauth') {
    return isMcpOAuthLoginUrl(mcpUrl) && (loginMode || tokenMaterial !== undefined)
  }
  if (authType === 'sigv4') return sigv4Material !== undefined
  return bearerValue !== ''
}

function secretMaterial(
  authType: BasicMcpServer['authType'],
  mcpUrl: string,
  tokenMaterial: ReturnType<typeof oauthTokenSetMaterial>,
  sigv4Material: ReturnType<typeof awsCredentialsMaterial>,
  bearerValue: string,
): { metadata?: { mcp_url: string }; material: SecretMaterial } | undefined {
  if (authType === 'oauth') {
    return (
      tokenMaterial && {
        metadata: { mcp_url: mcpUrl },
        material: { ...tokenMaterial, mcp_url: mcpUrl },
      }
    )
  }
  if (authType === 'sigv4') {
    return sigv4Material && { material: sigv4Material }
  }
  return { material: { kind: 'generic', value: bearerValue } }
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
  const [aws, setAws] = useState(newAWSCredentialsSecret)
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
  const sigv4Material = awsCredentialsMaterial(aws)
  const submitting = createSecret.isPending || login.pending
  const copy = dialogCopy[server.authType]
  const valid =
    name.trim() !== '' &&
    authMaterialValid(server.authType, mcpUrl, loginMode, tokenMaterial, sigv4Material, bearerValue)

  async function submit() {
    setError('')
    try {
      if (loginMode) {
        await login.start({ name, clientId, clientSecret })
        return
      }
      const secret = secretMaterial(
        server.authType,
        mcpUrl,
        tokenMaterial,
        sigv4Material,
        bearerValue,
      )
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
          <DialogTitle>{copy.title}</DialogTitle>
          <DialogDescription>{dialogDescription(server.authType, mcpUrl)}</DialogDescription>
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
                placeholder={copy.placeholder}
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
                {submitLabel(loginMode, mcpUrl)}
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
