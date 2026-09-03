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

import { AgentConfigMcpToolOverrideList } from '@/components/agents/AgentConfigMcpToolOverrideList'
import { mcpServerToolsRequest } from '@/components/agents/mcpServerToolsRequest'
import {
  type BasicMcpServer,
  type BasicMcpTool,
  type McpAuthType,
  mcpRuntimeToolName,
  mcpRuntimeToolNameError,
  mcpRuntimeToolNameMaxLength,
  mcpToolEnabled,
} from '@/components/agents/useAgentBuilderForm'
import { SearchIcon, TriangleAlert } from '@/components/icons'
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
import { useDebouncedValue } from '@/hooks/use-resource-list'

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
  const request = parseRequestKey(useDebouncedValue(JSON.stringify(mcpServerToolsRequest(server))))
  const discovery = useMcpServerTools(orgId, projectId, request, {
    onError: (error) => {
      const detected = detectedAuthType(error)
      if (server.authType === 'none' && detected != null) onAuthTypeChange(detected)
    },
  })
  const discovered = discovery.data?.tools ?? []
  const overriddenNames = new Set(server.tools.map((tool) => tool.name))
  const unexposableTools = discovered.flatMap((tool) => {
    if (!mcpToolEnabled(server, tool.name)) return []
    const error = mcpRuntimeToolNameError(server.name, tool.name)
    return error === undefined ? [] : [{ name: tool.name, error }]
  })
  const toolCountLabel = discovery.isSuccess
    ? `${discovered.length} ${discovered.length === 1 ? 'tool' : 'tools'}`
    : null
  const discoveryFailure = visibleDiscoveryFailure(discovery, server.authType)

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
        <ToolOverridePicker
          inputId={`${server.id}-tool-override`}
          discovered={discovered}
          overriddenNames={overriddenNames}
          discovering={discovery.isPending && request != null}
          toolCountLabel={toolCountLabel}
          onAdd={(name) => {
            onToolsChange([...server.tools, { name, enabled: null, permission: null }])
          }}
        />
        <AgentConfigMcpToolOverrideList
          tools={server.tools}
          discovered={discovered}
          permissionProfile={permissionProfile}
          toolCountLabel={toolCountLabel}
          onToolsChange={onToolsChange}
        />
      </div>
      {unexposableTools.length > 0 && (
        <UnexposableTools serverName={server.name} tools={unexposableTools} />
      )}
      {discoveryFailure && (
        <DiscoveryFailure
          error={discoveryFailure}
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

function ToolOverridePicker({
  inputId,
  discovered,
  overriddenNames,
  discovering,
  toolCountLabel,
  onAdd,
}: {
  inputId: string
  discovered: McpServerTool[]
  overriddenNames: Set<string>
  discovering: boolean
  toolCountLabel: string | null
  onAdd: (name: string) => void
}) {
  const [query, setQuery] = useState('')
  const candidates = discovered.filter((tool) => !overriddenNames.has(tool.name))
  const typedName = query.trim()
  const typedNameAddable =
    mcpToolNamePattern.test(typedName) &&
    !overriddenNames.has(typedName) &&
    !discovered.some((tool) => tool.name === typedName)

  function addOverride(name: string) {
    if (overriddenNames.has(name)) return
    onAdd(name)
    setQuery('')
  }

  return (
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
      isItemEqualToValue={(tool: McpServerTool, other: McpServerTool) => tool.name === other.name}
      filter={matchesToolSearch}
    >
      <div className="bg-muted/40 relative border-b">
        <SearchIcon className="text-muted-foreground pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2" />
        <ComboboxPrimitive.Input
          id={inputId}
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
          {discovering
            ? null
            : emptyMessage({
                typedName: typedNameAddable ? typedName : null,
                candidates,
                discovered,
              })}
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
        {discovering && <ComboboxLoading label="Discovering tools…" />}
      </ComboboxContent>
    </Combobox>
  )
}

function matchesToolSearch(tool: McpServerTool, search: string) {
  const needle = search.trim().toLowerCase()
  return (
    needle === '' ||
    tool.name.toLowerCase().includes(needle) ||
    (tool.title?.toLowerCase().includes(needle) ?? false) ||
    (tool.description?.toLowerCase().includes(needle) ?? false)
  )
}

function emptyMessage({
  typedName,
  candidates,
  discovered,
}: {
  typedName: string | null
  candidates: McpServerTool[]
  discovered: McpServerTool[]
}) {
  if (typedName != null) return `Press Enter to add "${typedName}"`
  if (candidates.length === 0 && discovered.length > 0) {
    return 'Every discovered tool already has an override.'
  }
  return 'No tools match.'
}

function visibleDiscoveryFailure(
  discovery: ReturnType<typeof useMcpServerTools>,
  authType: McpAuthType,
) {
  if (!discovery.isError) return null
  if (authType !== 'none') return discovery.error
  return detectedAuthType(discovery.error) == null ? discovery.error : null
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
            ? `Shorten the server name to ${maxServerNameLength} characters or fewer, or disable these tools.`
            : 'Some tool names are too long to expose under any server name, so disable them.'}
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
