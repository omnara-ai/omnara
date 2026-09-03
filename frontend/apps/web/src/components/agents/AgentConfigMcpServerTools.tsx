import { Combobox as ComboboxPrimitive } from '@base-ui/react/combobox'
import { useMcpServerTools } from '@omnara/react'
import {
  ApiError,
  type McpServerTool,
  type McpServerToolsRequest,
  schemas,
  type ToolPermissionProfile,
} from '@omnara/sdk'
import { useState } from 'react'

import { mcpServerToolsRequest } from '@/components/agents/mcpServerToolsRequest'
import {
  type BasicMcpServer,
  type BasicMcpTool,
  type McpAuthType,
  mcpRuntimeToolName,
  mcpRuntimeToolNameError,
  mcpRuntimeToolNameMaxLength,
} from '@/components/agents/useAgentBuilderForm'
import { SearchIcon, Trash2Icon, TriangleAlert } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  Combobox,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxItem,
  ComboboxList,
  ComboboxLoading,
} from '@/components/ui/combobox'
import { Field, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useDebouncedValue } from '@/hooks/use-resource-list'

const inheritValue = 'inherit'
const mcpToolNamePattern = /^[a-zA-Z][a-zA-Z0-9_-]{0,63}$/

export function AgentConfigMcpServerTools({
  orgId,
  projectId,
  server,
  permissionProfile,
  onToolsChange,
  onAuthTypeChange,
}: {
  orgId: string
  projectId: string
  server: BasicMcpServer
  permissionProfile?: ToolPermissionProfile
  onToolsChange: (tools: BasicMcpTool[]) => void
  onAuthTypeChange: (authType: McpAuthType) => void
}) {
  const { authType } = server
  const request = parseRequestKey(useDebouncedValue(JSON.stringify(mcpServerToolsRequest(server))))
  const discovery = useMcpServerTools(orgId, projectId, request, {
    onError: (error) => {
      const detected = detectedAuthType(error)
      if (authType === 'none' && detected != null) onAuthTypeChange(detected)
    },
  })
  const [query, setQuery] = useState('')
  const [openDescription, setOpenDescription] = useState<string | null>(null)

  const discovered = discovery.data?.tools ?? []
  const discoveredByName = new Map(discovered.map((tool) => [tool.name, tool]))
  const overriddenNames = new Set(server.tools.map((tool) => tool.name))
  const candidates = discovered.filter((tool) => !overriddenNames.has(tool.name))
  const typedName = query.trim()
  const typedNameAddable =
    mcpToolNamePattern.test(typedName) &&
    mcpRuntimeToolNameError(server.name, typedName) === undefined &&
    !overriddenNames.has(typedName) &&
    !discoveredByName.has(typedName)
  const unexposableTools = discovered.flatMap((tool) => {
    const error = mcpRuntimeToolNameError(server.name, tool.name)
    return error === undefined ? [] : [{ name: tool.name, error }]
  })

  function addOverride(name: string) {
    if (overriddenNames.has(name)) return
    onToolsChange([...server.tools, { name, enabled: null, permission: null }])
    setQuery('')
  }

  function updateOverride(name: string, patch: Partial<Omit<BasicMcpTool, 'name'>>) {
    onToolsChange(server.tools.map((tool) => (tool.name === name ? { ...tool, ...patch } : tool)))
  }

  const toolCountLabel = discovery.isSuccess
    ? `${discovered.length} ${discovered.length === 1 ? 'tool' : 'tools'}`
    : null
  return (
    <Field>
      <FieldLabel htmlFor={`${server.id}-tool-override`}>
        Tool overrides
        <span className="text-muted-foreground font-normal">
          {' '}
          — per-tool exceptions to the settings above
        </span>
      </FieldLabel>
      <div className="rounded-md border">
        <Combobox
          items={candidates}
          inputValue={query}
          onInputValueChange={(value, details) => {
            if (details.reason === 'input-change' || details.reason === 'input-clear') {
              setQuery(value)
            }
          }}
          value={null}
          onValueChange={(tool: McpServerTool | null) => {
            if (tool) addOverride(tool.name)
          }}
          itemToStringLabel={(tool: McpServerTool) => tool.name}
          itemToStringValue={(tool: McpServerTool) => tool.name}
          isItemEqualToValue={(tool: McpServerTool, other: McpServerTool) =>
            tool.name === other.name
          }
          filter={(tool: McpServerTool, search: string) => {
            const needle = search.trim().toLowerCase()
            return (
              needle === '' ||
              tool.name.toLowerCase().includes(needle) ||
              (tool.title?.toLowerCase().includes(needle) ?? false) ||
              (tool.description?.toLowerCase().includes(needle) ?? false)
            )
          }}
        >
          <div className="bg-muted/40 relative border-b">
            <SearchIcon className="text-muted-foreground pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2" />
            <ComboboxPrimitive.Input
              id={`${server.id}-tool-override`}
              className="placeholder:text-muted-foreground pointer-coarse:text-base h-11 w-full bg-transparent pl-10 pr-3 text-base outline-none md:text-sm"
              placeholder={
                toolCountLabel == null
                  ? 'Add a tool override'
                  : `Add a tool override — search ${toolCountLabel}`
              }
              onKeyDown={(event) => {
                if (event.key === 'Enter' && typedNameAddable) {
                  event.preventDefault()
                  addOverride(typedName)
                }
              }}
            />
          </div>
          <ComboboxContent>
            <ComboboxEmpty>
              {discovery.isPending && request != null
                ? null
                : typedNameAddable
                  ? `Press Enter to add "${typedName}"`
                  : candidates.length === 0 && discovered.length > 0
                    ? 'Every discovered tool already has an override.'
                    : 'No tools match.'}
            </ComboboxEmpty>
            <ComboboxList>
              {(tool: McpServerTool) => (
                <ComboboxItem key={tool.name} value={tool}>
                  <div className="min-w-0 flex-1">
                    <div className="truncate font-mono text-xs">{tool.name}</div>
                    {tool.description && (
                      <div className="text-muted-foreground line-clamp-2 text-xs">
                        {tool.description}
                      </div>
                    )}
                  </div>
                </ComboboxItem>
              )}
            </ComboboxList>
            {discovery.isPending && request != null && (
              <ComboboxLoading label="Discovering tools…" />
            )}
          </ComboboxContent>
        </Combobox>
        {server.tools.length === 0 ? (
          <p className="text-muted-foreground px-4 py-3 text-sm">
            {toolCountLabel == null
              ? 'No overrides.'
              : `No overrides — all ${toolCountLabel} follow the settings above.`}
          </p>
        ) : (
          <div className="divide-y">
            {server.tools.map((tool) => {
              const description = discoveredByName.get(tool.name)?.description
              return (
                <div
                  key={tool.name}
                  className="flex flex-wrap items-center gap-2 px-3 py-2 sm:flex-nowrap sm:gap-3"
                >
                  <div
                    className="-my-2 flex min-w-0 flex-1 basis-full items-center self-stretch py-2 sm:basis-auto"
                    onPointerEnter={() => {
                      setOpenDescription(tool.name)
                    }}
                    onPointerLeave={() => {
                      setOpenDescription(null)
                    }}
                  >
                    {description ? (
                      <Tooltip open={openDescription === tool.name}>
                        <TooltipTrigger asChild>
                          <button
                            type="button"
                            className="bg-muted block max-w-full cursor-default truncate rounded-md px-2 py-1 text-left font-mono text-xs outline-none focus-visible:ring-2"
                            aria-label={`About ${tool.name}`}
                            onFocus={() => {
                              setOpenDescription(tool.name)
                            }}
                            onBlur={() => {
                              setOpenDescription(null)
                            }}
                          >
                            {tool.name}
                          </button>
                        </TooltipTrigger>
                        <TooltipContent
                          side="right"
                          className="max-w-sm px-4 py-2 text-sm leading-relaxed"
                        >
                          {description}
                        </TooltipContent>
                      </Tooltip>
                    ) : (
                      <span className="bg-muted truncate rounded-md px-2 py-1 font-mono text-xs">
                        {tool.name}
                      </span>
                    )}
                  </div>
                  <Select
                    value={tool.enabled == null ? inheritValue : String(tool.enabled)}
                    onValueChange={(value) => {
                      updateOverride(tool.name, {
                        enabled: value === inheritValue ? null : value === 'true',
                      })
                    }}
                  >
                    <SelectTrigger
                      size="sm"
                      className="min-w-0 flex-1 sm:w-36 sm:flex-none"
                      aria-label={`${tool.name} enabled`}
                    >
                      <SelectValue>
                        {tool.enabled == null ? 'Default' : tool.enabled ? 'Enabled' : 'Disabled'}
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={inheritValue}>Default</SelectItem>
                      <SelectItem value="true">Enabled</SelectItem>
                      <SelectItem value="false">Disabled</SelectItem>
                    </SelectContent>
                  </Select>
                  <Select
                    value={tool.permission?.mode ?? inheritValue}
                    disabled={permissionProfile == null}
                    onValueChange={(mode) => {
                      updateOverride(tool.name, {
                        permission: mode === inheritValue ? null : { mode, parameters: {} },
                      })
                    }}
                  >
                    <SelectTrigger
                      size="sm"
                      className="min-w-0 flex-1 sm:w-40 sm:flex-none"
                      aria-label={`${tool.name} permission`}
                    >
                      <SelectValue>
                        {tool.permission == null
                          ? 'Default'
                          : permissionModeLabel(permissionProfile, tool.permission.mode)}
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={inheritValue}>Default</SelectItem>
                      {permissionProfile?.permission_modes.map((mode) => (
                        <SelectItem key={mode.name} value={mode.name}>
                          {mode.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <Button
                    type="button"
                    size="icon"
                    variant="ghost"
                    aria-label={`Remove ${tool.name} override`}
                    onClick={() => {
                      onToolsChange(
                        server.tools.filter((candidate) => candidate.name !== tool.name),
                      )
                    }}
                  >
                    <Trash2Icon />
                  </Button>
                </div>
              )
            })}
          </div>
        )}
      </div>
      {unexposableTools.length > 0 && (
        <UnexposableTools serverName={server.name} tools={unexposableTools} />
      )}
      {discovery.isError && (authType !== 'none' || detectedAuthType(discovery.error) == null) && (
        <DiscoveryFailure
          error={discovery.error}
          server={server}
          onRetry={() => {
            void discovery.refetch()
          }}
          onAuthTypeChange={onAuthTypeChange}
        />
      )}
    </Field>
  )
}

function parseRequestKey(key: string): McpServerToolsRequest | null {
  if (key === '' || key === 'null') return null
  const parsed = schemas.zMcpServerToolsRequest.safeParse(JSON.parse(key))
  return parsed.success ? parsed.data : null
}

function DiscoveryFailure({
  error,
  server,
  onRetry,
  onAuthTypeChange,
}: {
  error: unknown
  server: BasicMcpServer
  onRetry: () => void
  onAuthTypeChange: (authType: McpAuthType) => void
}) {
  const detected = detectedAuthType(error)
  const switchable = detected != null && detected !== server.authType
  return (
    <div role="alert" className="flex items-start gap-2 text-sm text-amber-600 dark:text-amber-500">
      <TriangleAlert className="mt-0.5 size-4 shrink-0" />
      <div className="min-w-0 flex-1 space-y-1">
        <p className="font-medium">{discoveryFailureTitle(error, server, detected)}</p>
        <p className="text-muted-foreground break-words">
          {error instanceof ApiError ? error.message : 'Unexpected error.'}
        </p>
      </div>
      <Button
        type="button"
        size="sm"
        variant="outline"
        className="shrink-0"
        onClick={() => {
          if (switchable) onAuthTypeChange(detected)
          else onRetry()
        }}
      >
        {switchable ? (detected === 'oauth' ? 'Use OAuth' : 'Use bearer secret') : 'Retry'}
      </Button>
    </div>
  )
}

function UnexposableTools({
  serverName,
  tools,
}: {
  serverName: string
  tools: { name: string; error: string }[]
}) {
  const longestToolName = Math.max(...tools.map((tool) => tool.name.length))
  const maxServerNameLength =
    mcpRuntimeToolNameMaxLength - mcpRuntimeToolName('', '').length - longestToolName
  return (
    <div role="alert" className="text-destructive flex items-start gap-2 text-sm">
      <TriangleAlert className="mt-0.5 size-4 shrink-0" />
      <div className="min-w-0 flex-1 space-y-1">
        <p className="font-medium">
          {tools.length === 1
            ? 'One tool cannot be exposed to the model, so the agent will fail to connect to this server.'
            : `${tools.length} tools cannot be exposed to the model, so the agent will fail to connect to this server.`}
        </p>
        <p className="text-muted-foreground">
          Omnara names MCP tools <code className="font-mono">mcp__{serverName}__&lt;tool&gt;</code>,
          and the full name must be {mcpRuntimeToolNameMaxLength} characters or fewer.{' '}
          {maxServerNameLength >= 1
            ? `Shorten the server name to ${maxServerNameLength} characters or fewer.`
            : 'Some tool names are too long to expose under any server name.'}
        </p>
        <ul className="text-muted-foreground list-disc space-y-0.5 pl-5">
          {tools.map((tool) => (
            <li key={tool.name} className="break-all font-mono text-xs">
              {tool.name}
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}

function detectedAuthType(cause: unknown): McpAuthType | null {
  if (!(cause instanceof ApiError) || cause.status !== 422) return null
  const parsed = schemas.zMcpServerAuthRequiredError.safeParse(cause.body)
  return parsed.success ? parsed.data.auth.type : null
}

function discoveryFailureTitle(
  cause: unknown,
  server: BasicMcpServer,
  detected: McpAuthType | null,
) {
  if (detected === 'oauth') {
    return server.authType === 'oauth'
      ? 'The server rejected the selected OAuth secret. Log in again to refresh it.'
      : 'This server uses OAuth. Switch authentication to an OAuth secret and log in.'
  }
  if (detected === 'bearer') {
    return server.authType === 'bearer' || server.authType === 'sigv4'
      ? 'The server rejected the selected secret.'
      : 'This server expects a bearer token. Switch authentication to a bearer secret.'
  }
  if (cause instanceof ApiError && cause.status === 502) {
    return 'Could not connect to the MCP server.'
  }
  if (cause instanceof ApiError && cause.status === 404) {
    return 'The selected secret is not available to this project.'
  }
  return 'Could not discover tools.'
}

function permissionModeLabel(profile: ToolPermissionProfile | undefined, value: string) {
  return profile?.permission_modes.find((mode) => mode.name === value)?.label ?? value
}
