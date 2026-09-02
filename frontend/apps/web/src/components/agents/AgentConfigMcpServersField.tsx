import { useServerInfo } from '@omnara/react'
import type { Secret, ToolPermissionProfile } from '@omnara/sdk'
import { useEffect, useRef, useState } from 'react'

import { AgentConfigMcpSecretCombobox } from '@/components/agents/AgentConfigMcpSecretCombobox'
import { AgentConfigMcpSecretDialog } from '@/components/agents/AgentConfigMcpSecretDialog'
import { AgentConfigMcpServerTools } from '@/components/agents/AgentConfigMcpServerTools'
import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import {
  defaultMcpSecretName,
  isMcpOAuthLoginUrl,
  useMcpOAuthLogin,
  useProjectSecretNamed,
} from '@/components/agents/mcpOAuthLogin'
import { savePendingMcpBuilderOAuth } from '@/components/agents/pendingMcpBuilderOAuth'
import {
  type BasicConfig,
  type BasicMcpServer,
  type McpAuthType,
  mcpServerNameError,
} from '@/components/agents/useAgentBuilderForm'
import { KeyRound, PlusIcon, Trash2Icon } from '@/components/icons'
import { OverridesCollapsible } from '@/components/machines/MachineOverrideFields'
import { registryServerLabel } from '@/components/mcp/mcpRegistry'
import { McpServerIdentityGroup } from '@/components/mcp/McpServerIdentityGroup'
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
import { useDebouncedValue } from '@/hooks/use-resource-list'
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
    tools: [],
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

  const pendingOAuthContextRef = useRef({ builderDraft, agentName })
  useEffect(() => {
    pendingOAuthContextRef.current = { builderDraft, agentName }
  })

  return (
    <AgentConfigSectionCard
      title="MCP servers"
      action={
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="text-muted-foreground size-10 sm:size-8"
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
              server.name === ''
                ? undefined
                : (mcpServerNameError(server.name) ??
                  (duplicateName ? 'Name must be unique within this configuration.' : undefined))
            return (
              <div key={server.id} className="space-y-4 px-5 py-4">
                <McpServerIdentityFields
                  server={server}
                  nameError={nameError}
                  onChange={(patch) => {
                    updateServer(server.id, patch)
                  }}
                  onRemove={() => {
                    onServersChange(servers.filter((candidate) => candidate.id !== server.id))
                  }}
                />
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
                          agentName: pendingOAuthContextRef.current.agentName,
                          draft: pendingOAuthContextRef.current.builderDraft,
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
                <div className="pb-2">
                  <OverridesCollapsible title="Tools" keepMounted>
                    <div className="space-y-4">
                      <div className="grid gap-4 sm:grid-cols-2">
                        <Field>
                          <FieldLabel>Default permission</FieldLabel>
                          <Select
                            value={
                              server.permission?.mode ??
                              permissionProfile?.default_permission.mode ??
                              ''
                            }
                            disabled={permissionProfile == null}
                            onValueChange={(mode) => {
                              if (mode === '') return
                              updateServer(server.id, { permission: { mode, parameters: {} } })
                            }}
                          >
                            <SelectTrigger className="w-full" aria-label="MCP default permission">
                              <SelectValue>
                                {permissionModeLabel(
                                  permissionProfile,
                                  server.permission?.mode ??
                                    permissionProfile?.default_permission.mode,
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
                          <FieldLabel>Default visibility</FieldLabel>
                          <Select
                            value={server.defaultEnabled ? 'true' : 'false'}
                            onValueChange={(value) => {
                              updateServer(server.id, { defaultEnabled: value === 'true' })
                            }}
                          >
                            <SelectTrigger className="w-full" aria-label="MCP default visibility">
                              <SelectValue>
                                {server.defaultEnabled ? 'Enabled' : 'Disabled'}
                              </SelectValue>
                            </SelectTrigger>
                            <SelectContent>
                              <SelectItem value="true">Enabled</SelectItem>
                              <SelectItem value="false">Disabled</SelectItem>
                            </SelectContent>
                          </Select>
                        </Field>
                      </div>
                      <AgentConfigMcpServerTools
                        orgId={orgId}
                        projectId={projectId}
                        server={server}
                        permissionProfile={permissionProfile}
                        onToolsChange={(tools) => {
                          updateServer(server.id, { tools })
                        }}
                        onAuthTypeChange={(authType) => {
                          updateServer(server.id, {
                            authType,
                            secretId: '',
                            service: '',
                            region: '',
                          })
                        }}
                      />
                    </div>
                  </OverridesCollapsible>
                </div>
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
  const loginSecretName = defaultMcpSecretName(server)
  const existingLoginSecret = useProjectSecretNamed(orgId, projectId, loginSecretName)
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
            warning: existingLoginSecret
              ? `This will update the existing secret "${existingLoginSecret.name}"`
              : undefined,
            onSelect: () => {
              login.start({ name: loginSecretName }).catch((cause: unknown) => {
                setDialog({ error: errorMessage(cause, 'Could not start login') })
              })
            },
          },
          addSecret,
        ]
      : [addSecret]

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

function McpServerIdentityFields({
  server,
  nameError,
  onChange,
  onRemove,
}: {
  server: BasicMcpServer
  nameError: string | undefined
  onChange: (patch: Partial<BasicMcpServer>) => void
  onRemove: () => void
}) {
  const info = useServerInfo(useDebouncedValue(server.url))
  const registryServer = info.data ?? null
  return (
    <Field data-invalid={nameError !== undefined}>
      <RequiredFieldLabel htmlFor={`${server.id}-name`}>Server</RequiredFieldLabel>
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <McpServerIdentityGroup
            idPrefix={server.id}
            name={server.name}
            nameInvalid={nameError !== undefined}
            url={server.url}
            onChange={(patch) => {
              const next: Partial<BasicMcpServer> = {}
              if (patch.name !== undefined) next.name = patch.name
              if (patch.url !== undefined && patch.url !== server.url) {
                next.url = patch.url
                next.secretId = ''
              }
              onChange(next)
            }}
          />
        </div>
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="shrink-0"
          aria-label="Remove MCP server"
          onClick={onRemove}
        >
          <Trash2Icon />
        </Button>
      </div>
      {registryServer && (
        <FieldDescription className="truncate">
          {registryServerLabel(registryServer)}
          {registryServer.description ? ` — ${registryServer.description}` : ''}
        </FieldDescription>
      )}
      <FieldError>{nameError}</FieldError>
    </Field>
  )
}
