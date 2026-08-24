import type { Secret, ToolPermissionProfile } from '@omnara/sdk'
import { useState } from 'react'

import { AgentConfigMcpSecretCombobox } from '@/components/agents/AgentConfigMcpSecretCombobox'
import { AgentConfigMcpSecretDialog } from '@/components/agents/AgentConfigMcpSecretDialog'
import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import {
  defaultMcpSecretName,
  isMcpOAuthLoginUrl,
  useMcpOAuthLogin,
} from '@/components/agents/mcpOAuthLogin'
import { savePendingMcpBuilderOAuth } from '@/components/agents/pendingMcpBuilderOAuth'
import {
  type BasicConfig,
  type BasicMcpServer,
  type McpAuthType,
  mcpServerNameError,
  mcpServerNameMaxLength,
} from '@/components/agents/useAgentBuilderForm'
import { KeyRound, PlusIcon, Trash2Icon } from '@/components/icons'
import { McpOAuthOutcomeDialog } from '@/components/secrets/McpOAuthOutcomeDialog'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
  RequiredFieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { errorMessage } from '@/lib/submit-status'

const awsSigningFields = [
  { key: 'service', label: 'Signing service' },
  { key: 'region', label: 'Signing region' },
] as const

const mcpAuthTypeOptions: { value: McpAuthType; label: string }[] = [
  { value: 'none', label: 'None' },
  { value: 'oauth', label: 'OAuth secret' },
  { value: 'bearer', label: 'Bearer secret' },
  { value: 'sigv4', label: 'AWS Signature V4' },
]

function newMcpServer(permissionProfile: ToolPermissionProfile): BasicMcpServer {
  return {
    id: crypto.randomUUID(),
    name: '',
    url: '',
    permission: structuredClone(permissionProfile.default_permission),
    defaultEnabled: true,
    authType: 'none',
    secretId: '',
    service: '',
    region: '',
  }
}

export function AgentConfigMcpServersField({
  orgId,
  projectId,
  permissionProfile,
  servers,
  onServersChange,
  builderDraft,
  agentName = null,
}: {
  orgId: string
  projectId: string
  permissionProfile?: ToolPermissionProfile
  servers: BasicMcpServer[]
  onServersChange: (servers: BasicMcpServer[]) => void
  builderDraft: BasicConfig
  agentName?: string | null
}) {
  function updateServer(id: string, patch: Partial<BasicMcpServer>) {
    onServersChange(servers.map((server) => (server.id === id ? { ...server, ...patch } : server)))
  }

  return (
    <AgentConfigSectionCard
      title="MCP servers"
      action={
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="text-muted-foreground size-8"
          disabled={permissionProfile == null}
          aria-label="Add server"
          onClick={() => {
            if (permissionProfile != null) {
              onServersChange([...servers, newMcpServer(permissionProfile)])
            }
          }}
        >
          <PlusIcon />
        </Button>
      }
    >
      {servers.length > 0 ? (
        <div className="divide-y">
          {servers.map((server) => {
            const duplicateName = servers.some(
              (candidate) => candidate.id !== server.id && candidate.name === server.name,
            )
            const nameError =
              mcpServerNameError(server.name) ??
              (duplicateName ? 'Name must be unique within this configuration.' : undefined)
            return (
              <div key={server.id} className="space-y-4 px-5 py-4">
                <div className="grid gap-4 sm:grid-cols-[minmax(8rem,14rem)_1fr_auto]">
                  <Field data-invalid={nameError !== undefined}>
                    <RequiredFieldLabel htmlFor={`${server.id}-name`}>Name</RequiredFieldLabel>
                    <Input
                      id={`${server.id}-name`}
                      required
                      maxLength={mcpServerNameMaxLength}
                      aria-invalid={nameError !== undefined}
                      value={server.name}
                      placeholder="github"
                      onChange={(event) => {
                        updateServer(server.id, { name: event.target.value })
                      }}
                    />
                    <FieldError>{nameError}</FieldError>
                  </Field>
                  <Field>
                    <RequiredFieldLabel htmlFor={`${server.id}-url`}>URL</RequiredFieldLabel>
                    <Input
                      id={`${server.id}-url`}
                      required
                      value={server.url}
                      placeholder="https://example.com/mcp"
                      onChange={(event) => {
                        updateServer(server.id, { url: event.target.value, secretId: '' })
                      }}
                    />
                  </Field>
                  <div className="hidden items-end sm:flex">
                    <Button
                      type="button"
                      size="icon"
                      variant="ghost"
                      aria-label="Remove MCP server"
                      onClick={() => {
                        onServersChange(servers.filter((candidate) => candidate.id !== server.id))
                      }}
                    >
                      <Trash2Icon />
                    </Button>
                  </div>
                </div>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field>
                    <FieldLabel>Tool permission</FieldLabel>
                    <Select
                      value={
                        server.permission?.mode ?? permissionProfile?.default_permission.mode ?? ''
                      }
                      disabled={permissionProfile == null}
                      onValueChange={(mode) => {
                        updateServer(server.id, { permission: { mode, parameters: {} } })
                      }}
                    >
                      <SelectTrigger className="w-full" aria-label="MCP tool permission">
                        <SelectValue>
                          {permissionModeLabel(
                            permissionProfile,
                            server.permission?.mode ?? permissionProfile?.default_permission.mode,
                          )}
                        </SelectValue>
                      </SelectTrigger>
                      <SelectContent>
                        {permissionProfile?.permission_modes.map((mode) => (
                          <SelectItem key={mode.name} value={mode.name}>
                            {mode.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                  <Field>
                    <FieldLabel>Discovered tools</FieldLabel>
                    <Select
                      value={server.defaultEnabled ? 'true' : 'false'}
                      onValueChange={(value) => {
                        updateServer(server.id, { defaultEnabled: value === 'true' })
                      }}
                    >
                      <SelectTrigger className="w-full" aria-label="MCP default enable">
                        <SelectValue>
                          {server.defaultEnabled ? 'Enabled by default' : 'Disabled by default'}
                        </SelectValue>
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="true">Enabled by default</SelectItem>
                        <SelectItem value="false">Disabled by default</SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                </div>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field>
                    <FieldLabel>Authentication</FieldLabel>
                    <Select
                      value={server.authType}
                      onValueChange={(authType: McpAuthType) => {
                        updateServer(server.id, {
                          authType,
                          secretId: '',
                          service: '',
                          region: '',
                        })
                      }}
                    >
                      <SelectTrigger className="w-full" aria-label="MCP auth type">
                        <SelectValue>
                          {mcpAuthTypeOptions.find((option) => option.value === server.authType)
                            ?.label ?? server.authType}
                        </SelectValue>
                      </SelectTrigger>
                      <SelectContent>
                        {mcpAuthTypeOptions.map((option) => (
                          <SelectItem key={option.value} value={option.value}>
                            {option.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                  {server.authType !== 'none' && (
                    <McpServerSecretField
                      key={server.authType}
                      orgId={orgId}
                      projectId={projectId}
                      server={server}
                      onSecretChange={(secretId) => {
                        updateServer(server.id, { secretId })
                      }}
                      onBeforeOAuthRedirect={() => {
                        savePendingMcpBuilderOAuth({
                          returnPath: window.location.pathname,
                          serverId: server.id,
                          agentName,
                          draft: builderDraft,
                        })
                      }}
                    />
                  )}
                  {server.authType === 'sigv4' &&
                    awsSigningFields.map((field) => (
                      <Field key={field.key}>
                        <RequiredFieldLabel htmlFor={`${server.id}-aws-${field.key}`}>
                          {field.label}
                        </RequiredFieldLabel>
                        <Input
                          id={`${server.id}-aws-${field.key}`}
                          required
                          value={server[field.key]}
                          onChange={(event) => {
                            updateServer(server.id, { [field.key]: event.target.value })
                          }}
                        />
                      </Field>
                    ))}
                </div>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  className="sm:hidden"
                  onClick={() => {
                    onServersChange(servers.filter((candidate) => candidate.id !== server.id))
                  }}
                >
                  <Trash2Icon />
                  Remove server
                </Button>
              </div>
            )
          })}
        </div>
      ) : null}
      <McpOAuthOutcomeDialog />
    </AgentConfigSectionCard>
  )
}

function McpServerSecretField({
  orgId,
  projectId,
  server,
  onSecretChange,
  onBeforeOAuthRedirect,
}: {
  orgId: string
  projectId: string
  server: BasicMcpServer
  onSecretChange: (secretId: string) => void
  onBeforeOAuthRedirect: () => void
}) {
  const login = useMcpOAuthLogin({
    orgId,
    projectId,
    server,
    onBeforeRedirect: onBeforeOAuthRedirect,
  })
  const [dialog, setDialog] = useState<{ error: string } | null>(null)
  const [createdSecret, setCreatedSecret] = useState<Secret>()
  const mcpUrl = server.url.trim()
  const addSecret = {
    label: server.authType === 'oauth' ? 'Add secret (advanced)' : 'Add secret',
    icon: <PlusIcon className="size-4 shrink-0" />,
    onSelect: () => {
      setDialog({ error: '' })
    },
  }
  const actions =
    server.authType === 'oauth'
      ? [
          {
            label: `Login to ${mcpUrl || 'MCP server'}`,
            icon: <KeyRound className="size-4 shrink-0" />,
            disabled: !isMcpOAuthLoginUrl(mcpUrl) || login.pending,
            onSelect: () => {
              login.start({ name: defaultMcpSecretName(server) }).catch((error: unknown) => {
                setDialog({ error: errorMessage(error, 'Could not start login') })
              })
            },
          },
          addSecret,
        ]
      : server.authType === 'bearer'
        ? [addSecret]
        : []

  return (
    <Field>
      <RequiredFieldLabel htmlFor={`${server.id}-secret`}>Secret</RequiredFieldLabel>
      <AgentConfigMcpSecretCombobox
        id={`${server.id}-secret`}
        required
        orgId={orgId}
        projectId={projectId}
        server={server}
        knownSecret={createdSecret}
        onChange={onSecretChange}
        actions={actions}
      />
      <FieldDescription>
        {login.pending
          ? 'Starting login…'
          : server.authType === 'oauth'
            ? 'OAuth token sets whose MCP URL matches this server URL.'
            : server.authType === 'sigv4'
              ? 'AWS credentials visible to this project.'
              : 'Any generic secret visible to this project.'}
      </FieldDescription>
      {dialog && (
        <AgentConfigMcpSecretDialog
          orgId={orgId}
          projectId={projectId}
          server={server}
          initialError={dialog.error}
          onClose={() => {
            setDialog(null)
          }}
          onCreated={(secret) => {
            setCreatedSecret(secret)
            setDialog(null)
            onSecretChange(secret.id)
          }}
          onBeforeOAuthRedirect={onBeforeOAuthRedirect}
        />
      )}
    </Field>
  )
}

function permissionModeLabel(profile: ToolPermissionProfile | undefined, value?: string) {
  return profile?.permission_modes.find((mode) => mode.name === value)?.label ?? value ?? ''
}
