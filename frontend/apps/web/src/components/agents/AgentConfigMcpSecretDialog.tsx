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
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
  RequiredFieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { newOAuthTokenSetEntries, type OAuthEntry, oauthTokenSetMaterial } from '@/lib/oauthEntries'
import { errorMessage } from '@/lib/submit-status'

type OAuthMethod = 'login' | 'tokens'

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
  const [name, setName] = useState(() => (oauth ? defaultMcpSecretName(server) : ''))
  const [bearerValue, setBearerValue] = useState('')
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
  const submitting = createSecret.isPending || login.pending
  const valid =
    name.trim() !== '' &&
    (!oauth
      ? bearerValue !== ''
      : loginMode
        ? isMcpOAuthLoginUrl(mcpUrl)
        : tokenMaterial !== undefined)

  async function submit() {
    setError('')
    try {
      if (loginMode) {
        await login.start({ name, clientId, clientSecret })
        return
      }
      const material = oauth
        ? tokenMaterial && { ...tokenMaterial, mcp_url: mcpUrl }
        : ({ kind: 'generic', value: bearerValue } as const)
      if (material === undefined) return
      onCreated(await createSecret.mutateAsync({ owner, name: name.trim(), material }))
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
          <DialogTitle>{oauth ? 'Add OAuth secret' : 'Add bearer secret'}</DialogTitle>
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
                placeholder="mcp-token"
                onChange={(event) => {
                  setName(event.target.value)
                }}
              />
            </Field>
            {!oauth ? (
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
              <div className="grid gap-4 sm:grid-cols-2">
                <Field>
                  <FieldLabel htmlFor={`${idPrefix}-client-id`}>Client ID</FieldLabel>
                  <Input
                    id={`${idPrefix}-client-id`}
                    value={clientId}
                    autoComplete="off"
                    onChange={(event) => {
                      setClientId(event.target.value)
                    }}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor={`${idPrefix}-client-secret`}>Client secret</FieldLabel>
                  <Input
                    id={`${idPrefix}-client-secret`}
                    type="password"
                    value={clientSecret}
                    autoComplete="new-password"
                    onChange={(event) => {
                      setClientSecret(event.target.value)
                    }}
                  />
                </Field>
              </div>
            ) : (
              <OAuthTokenFields entries={entries} onChange={setEntries} hiddenKeys={['mcp_url']} />
            )}
            {loginMode && (
              <FieldDescription>
                {isMcpOAuthLoginUrl(mcpUrl)
                  ? 'Client credentials are only needed when the server requires a registered OAuth client.'
                  : 'Enter an https:// server URL to log in.'}
              </FieldDescription>
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
