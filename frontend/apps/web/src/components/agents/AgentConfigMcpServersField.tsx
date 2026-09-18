import { useServerInfo } from '@omnara/react'
import type { Secret, ToolPermissionProfile } from '@omnara/sdk'
import { useEffect, useRef, useState } from 'react'

import { permissionSelection } from '@/components/agents/agentConfigBasicExtract'
import { AgentConfigMcpSecretCombobox } from '@/components/agents/AgentConfigMcpSecretCombobox'
import { AgentConfigMcpSecretDialog } from '@/components/agents/AgentConfigMcpSecretDialog'
import { AgentConfigMcpServerTools } from '@/components/agents/AgentConfigMcpServerTools'
import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import {
  mcpAvailabilityOptions,
  mcpServerAvailability,
  mcpServerAvailabilityPatch,
  parseMcpAvailability,
} from '@/components/agents/mcpAvailability'
import {
  defaultMcpSecretName,
  isMcpOAuthLoginUrl,
  useMcpOAuthLogin,
  useProjectSecretNamed,
} from '@/components/agents/mcpOAuthLogin'
import { savePendingMcpBuilderOAuth } from '@/components/agents/pendingMcpBuilderOAuth'
import { PermissionModeGroup } from '@/components/agents/PermissionModeGroup'
import { permissionModeOptions } from '@/components/agents/permissionModeOptions'
import {
  type BasicConfig,
  type BasicMcpServer,
  type McpAuthType,
  mcpServerNameError,
} from '@/components/agents/useAgentBuilderForm'
import { ChevronRightIcon, KeyRound, PlusIcon, Trash2Icon } from '@/components/icons'
import { registryServerLabel } from '@/components/mcp/mcpRegistry'
import { McpServerIdentityGroup } from '@/components/mcp/McpServerIdentityGroup'
import { McpOAuthOutcomeDialog } from '@/components/secrets/McpOAuthOutcomeDialog'
import { Button } from '@/components/ui/button'
import { CollapseBody } from '@/components/ui/collapse-body'
import { Collapsible, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Field, FieldError, FieldLabel, RequiredFieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { useDebouncedValue } from '@/hooks/use-resource-list'
import { errorMessage } from '@/lib/submit-status'
import { cn } from '@/lib/utils'

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
    permission: permissionSelection(permissionProfile.default_permission),
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
  onBeforeOAuthRedirect,
}: {
  orgId: string
  projectId: string
  permissionProfile?: ToolPermissionProfile
  servers: BasicMcpServer[]
  onServersChange: (servers: BasicMcpServer[]) => void
  builderDraft: BasicConfig
  agentName?: string | null
  onBeforeOAuthRedirect?: () => void
}) {
  function updateServer(id: string, patch: Partial<BasicMcpServer>) {
    onServersChange(servers.map((server) => (server.id === id ? { ...server, ...patch } : server)))
  }

  const [expandedIds, setExpandedIds] = useState<ReadonlySet<string>>(new Set())
  function setExpanded(id: string, expanded: boolean) {
    setExpandedIds((prev) => {
      const next = new Set(prev)
      if (expanded) {
        next.add(id)
      } else {
        next.delete(id)
      }
      return next
    })
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
            if (permissionProfile == null) return
            const server = newMcpServer(permissionProfile)
            setExpanded(server.id, true)
            onServersChange([...servers, server])
          }}
        >
          <PlusIcon />
        </Button>
      }
    >
      {servers.length > 0 ? (
        <div className="flex flex-col gap-1 px-3 pb-3">
          {servers.map((server) => (
            <McpServerRow
              key={server.id}
              orgId={orgId}
              projectId={projectId}
              server={server}
              servers={servers}
              permissionProfile={permissionProfile}
              expanded={expandedIds.has(server.id)}
              onExpandedChange={(expanded) => {
                setExpanded(server.id, expanded)
              }}
              onChange={(patch) => {
                updateServer(server.id, patch)
              }}
              onRemove={() => {
                onServersChange(servers.filter((candidate) => candidate.id !== server.id))
              }}
              onBeforeOAuthRedirect={() => {
                savePendingMcpBuilderOAuth({
                  returnPath: window.location.pathname,
                  serverId: server.id,
                  agentName: pendingOAuthContextRef.current.agentName,
                  draft: pendingOAuthContextRef.current.builderDraft,
                })
                onBeforeOAuthRedirect?.()
              }}
            />
          ))}
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
function McpServerRow({
  orgId,
  projectId,
  server,
  servers,
  permissionProfile,
  expanded,
  onExpandedChange,
  onChange,
  onRemove,
  onBeforeOAuthRedirect,
}: {
  orgId: string
  projectId: string
  server: BasicMcpServer
  servers: BasicMcpServer[]
  permissionProfile?: ToolPermissionProfile
  expanded: boolean
  onExpandedChange: (expanded: boolean) => void
  onChange: (patch: Partial<BasicMcpServer>) => void
  onRemove: () => void
  onBeforeOAuthRedirect: () => void
}) {
  const info = useServerInfo(useDebouncedValue(server.url))
  const registryServer = info.data ?? null
  const duplicateName = servers.some(
    (candidate) => candidate.id !== server.id && candidate.name === server.name,
  )
  const nameError =
    server.name === ''
      ? undefined
      : (mcpServerNameError(server.name) ??
        (duplicateName ? 'Name must be unique within this configuration.' : undefined))
  return (
    <Collapsible
      open={expanded}
      onOpenChange={onExpandedChange}
      className="expanded-surface rounded-xl"
    >
      <div className="flex items-center gap-2 py-2 pl-1 pr-2">
        <CollapsibleTrigger asChild>
          <Button
            type="button"
            size="icon"
            variant="ghost"
            className="text-muted-foreground group size-10 sm:size-8"
            aria-label="Toggle server details"
          >
            <ChevronRightIcon className="size-4 transition-transform group-data-[state=open]:rotate-90" />
          </Button>
        </CollapsibleTrigger>
        <div className="min-w-0 flex-1">
          <McpServerIdentityGroup
            idPrefix={server.id}
            name={server.name}
            nameInvalid={nameError !== undefined}
            url={server.url}
            onChange={(patch) => {
              onExpandedChange(true)
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
          className="text-muted-foreground size-10 sm:size-8"
          aria-label="Remove MCP server"
          onClick={onRemove}
        >
          <Trash2Icon />
        </Button>
      </div>
      <CollapseBody open={expanded}>
        <div className="flex flex-col gap-4 px-3 pb-5 pt-5 sm:pl-11">
          {nameError && <FieldError>{nameError}</FieldError>}
          {registryServer ? (
            <p className="text-muted-foreground -mt-3 text-sm">
              {registryServerLabel(registryServer)}
              {registryServer.description ? ` — ${registryServer.description}` : ''}
            </p>
          ) : (
            <Skeleton
              className={cn(
                '-mt-3 h-5 w-2/3',
                (server.url.trim() === '' || !info.isPending) && 'animate-none',
              )}
            />
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field>
              <FieldLabel>Authentication</FieldLabel>
              <Select
                value={server.authType}
                onValueChange={(authType: McpAuthType) => {
                  onChange({ authType, secretId: '', service: '', region: '' })
                }}
              >
                <SelectTrigger className="w-full" aria-label="MCP auth type">
                  <SelectValue>
                    {mcpAuthTypeOptions.find((option) => option.value === server.authType)?.label ??
                      server.authType}
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
                  onChange({ secretId })
                }}
                onBeforeOAuthRedirect={onBeforeOAuthRedirect}
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
                      onChange({ [field.key]: event.target.value })
                    }}
                  />
                </Field>
              ))}
          </div>
          <div>
            <AgentConfigMcpServerTools
              orgId={orgId}
              projectId={projectId}
              server={server}
              permissionProfile={permissionProfile}
              defaults={
                <>
                  <Field className="w-36 shrink-0 items-center">
                    <FieldLabel className="justify-center whitespace-nowrap">
                      Default Permission
                    </FieldLabel>
                    <div className="flex justify-center">
                      <PermissionModeGroup
                        label="MCP default permission"
                        className="w-32 [&>button]:flex-1"
                        options={permissionModeOptions(
                          permissionProfile?.permission_modes,
                          server.permission?.mode,
                        )}
                        value={
                          server.permission?.mode ??
                          permissionProfile?.default_permission.mode ??
                          ''
                        }
                        disabled={permissionProfile == null}
                        onChange={(mode) => {
                          onChange({ permission: { mode, parameters: {} } })
                        }}
                      />
                    </div>
                  </Field>
                  <Field className="w-36 shrink-0 items-center">
                    <FieldLabel className="justify-center whitespace-nowrap">
                      Default Visibility
                    </FieldLabel>
                    <div className="flex justify-center">
                      <PermissionModeGroup
                        label="MCP default visibility"
                        className="w-32 [&>button]:flex-1"
                        options={mcpAvailabilityOptions}
                        value={mcpServerAvailability(server)}
                        onChange={(value) => {
                          const availability = parseMcpAvailability(value)
                          if (availability == null) return
                          onChange(mcpServerAvailabilityPatch(availability))
                        }}
                      />
                    </div>
                  </Field>
                </>
              }
              onToolsChange={(tools) => {
                onChange({ tools })
              }}
              onAuthTypeChange={(authType) => {
                onChange({ authType, secretId: '', service: '', region: '' })
              }}
            />
          </div>
        </div>
      </CollapseBody>
    </Collapsible>
  )
}
