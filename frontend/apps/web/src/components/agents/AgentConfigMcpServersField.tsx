import type { ToolPermissionProfile } from '@omnara/sdk'
import { Trash2Icon } from 'lucide-react'

import { AgentConfigMcpSecretCombobox } from '@/components/agents/AgentConfigMcpSecretCombobox'
import type { BasicMcpServer, McpAuthType } from '@/components/agents/useAgentBuilderForm'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
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
    <Field>
      <div className="flex items-center justify-between gap-3">
        <div>
          <FieldLabel>MCP servers</FieldLabel>
          <FieldDescription>Remote MCP servers the agent can call tools on.</FieldDescription>
        </div>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={permissionProfile == null}
          onClick={() => {
            if (permissionProfile != null) {
              onServersChange([...servers, newMcpServer(permissionProfile)])
            }
          }}
        >
          Add server
        </Button>
      </div>
      <div className="space-y-3">
        {servers.length === 0 ? (
          <div className="border-border bg-background/60 text-muted-foreground flex min-h-16 items-center justify-center rounded-md border border-dashed px-4 text-sm">
            No MCP servers
          </div>
        ) : (
          servers.map((server) => {
            return (
              <div
                key={server.id}
                className="border-border bg-background space-y-4 rounded-lg border p-4"
              >
                <div className="grid gap-4 sm:grid-cols-[minmax(8rem,14rem)_1fr_auto]">
                  <Field>
                    <FieldLabel htmlFor={`${server.id}-name`}>Name</FieldLabel>
                    <Input
                      id={`${server.id}-name`}
                      value={server.name}
                      placeholder="github"
                      onChange={(event) => {
                        updateServer(server.id, { name: event.target.value })
                      }}
                    />
                  </Field>
                  <Field>
                    <FieldLabel htmlFor={`${server.id}-url`}>URL</FieldLabel>
                    <Input
                      id={`${server.id}-url`}
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
                      value={server.permission.mode}
                      disabled={permissionProfile == null}
                      onValueChange={(mode) => {
                        updateServer(server.id, { permission: { mode, parameters: {} } })
                      }}
                    >
                      <SelectTrigger className="w-full" aria-label="MCP tool permission">
                        <SelectValue>
                          {permissionModeLabel(permissionProfile, server.permission.mode)}
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
                        <SelectValue />
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
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="none">None</SelectItem>
                        <SelectItem value="oauth">OAuth secret</SelectItem>
                        <SelectItem value="bearer">Bearer secret</SelectItem>
                        <SelectItem value="sigv4">AWS Signature V4</SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                  {server.authType !== 'none' && (
                    <Field>
                      <FieldLabel>Secret</FieldLabel>
                      <AgentConfigMcpSecretCombobox
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
                        <FieldLabel htmlFor={`${server.id}-aws-${field.key}`}>
                          {field.label}
                        </FieldLabel>
                        <Input
                          id={`${server.id}-aws-${field.key}`}
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
          })
        )}
      </div>
    </Field>
  )
}

function permissionModeLabel(profile: ToolPermissionProfile | undefined, value: string) {
  return profile?.permission_modes.find((mode) => mode.name === value)?.label ?? value
}
