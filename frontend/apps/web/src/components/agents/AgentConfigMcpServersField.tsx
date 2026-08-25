import type { ToolPermissionProfile } from '@omnara/sdk'

import { AgentConfigMcpSecretCombobox } from '@/components/agents/AgentConfigMcpSecretCombobox'
import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import {
  type BasicMcpServer,
  type McpAuthType,
  mcpServerNameError,
  mcpServerNameMaxLength,
} from '@/components/agents/useAgentBuilderForm'
import { PlusIcon, Trash2Icon } from '@/components/icons'
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
}: {
  orgId: string
  projectId: string
  permissionProfile?: ToolPermissionProfile
  servers: BasicMcpServer[]
  onServersChange: (servers: BasicMcpServer[]) => void
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
                    <Field>
                      <RequiredFieldLabel htmlFor={`${server.id}-secret`}>
                        Secret
                      </RequiredFieldLabel>
                      <AgentConfigMcpSecretCombobox
                        id={`${server.id}-secret`}
                        required
                        orgId={orgId}
                        projectId={projectId}
                        server={server}
                        onChange={(secretId) => {
                          updateServer(server.id, { secretId })
                        }}
                      />
                      <FieldDescription>
                        {server.authType === 'oauth'
                          ? 'OAuth token sets whose MCP URL matches this server URL.'
                          : server.authType === 'sigv4'
                            ? 'AWS credentials visible to this project.'
                            : 'Any generic secret visible to this project.'}
                      </FieldDescription>
                    </Field>
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
    </AgentConfigSectionCard>
  )
}

function permissionModeLabel(profile: ToolPermissionProfile | undefined, value?: string) {
  return profile?.permission_modes.find((mode) => mode.name === value)?.label ?? value ?? ''
}
