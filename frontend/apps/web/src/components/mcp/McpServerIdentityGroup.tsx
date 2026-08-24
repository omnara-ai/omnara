import { useServerInfo, useServers } from '@omnara/react'
import { type FocusEvent, type KeyboardEvent, useEffect, useId, useState } from 'react'

import { mcpServerNameMaxLength } from '@/components/agents/useAgentBuilderForm'
import {
  registryServerEntries,
  type RegistryServerEntry,
  registryServerLabel,
  registryServerSearchFilters,
  registryServerShortName,
} from '@/components/mcp/mcpRegistry'
import { McpServerIcon } from '@/components/mcp/McpServerIcon'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useDebouncedValue } from '@/hooks/use-resource-list'
import { cn } from '@/lib/utils'

const inputClassName =
  'placeholder:text-muted-foreground h-9 min-w-0 flex-1 bg-transparent px-3 text-base outline-none md:text-sm'

export function McpServerIdentityGroup({
  idPrefix,
  name,
  nameInvalid,
  url,
  onChange,
}: {
  idPrefix: string
  name: string
  nameInvalid: boolean
  url: string
  onChange: (patch: { name?: string; url: string }) => void
}) {
  const listId = useId()
  const [open, setOpen] = useState(false)
  const [nameFocused, setNameFocused] = useState(false)
  const [highlighted, setHighlighted] = useState(0)
  const debouncedName = useDebouncedValue(name)
  const filters = registryServerSearchFilters(debouncedName)
  const searching = filters.q !== undefined
  const serversQuery = useServers({ filters, pageSize: 25, enabled: open && searching })
  const results = registryServerEntries(useInfiniteQueryItems(serversQuery))
  const info = useServerInfo(url)
  const selected = info.data ?? null
  const autofillName =
    name === '' && !nameFocused && selected ? registryServerShortName(selected) : null

  useEffect(() => {
    if (autofillName !== null) {
      onChange({ name: autofillName, url })
    }
  }, [autofillName, onChange, url])
  const showList = open && searching
  const activeIndex = Math.min(highlighted, Math.max(results.length - 1, 0))

  function select(entry: RegistryServerEntry) {
    onChange({
      name: name.trim() === '' && !nameFocused ? registryServerShortName(entry.server) : undefined,
      url: entry.remote.url,
    })
    setOpen(false)
  }

  function onBlur(event: FocusEvent<HTMLDivElement>) {
    if (!event.currentTarget.contains(event.relatedTarget)) {
      setOpen(false)
    }
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Escape') {
      setOpen(false)
      return
    }
    if (!showList || results.length === 0) return
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setOpen(true)
      setHighlighted((activeIndex + 1) % results.length)
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      setHighlighted((activeIndex - 1 + results.length) % results.length)
    } else if (event.key === 'Enter') {
      const entry = results[activeIndex]
      if (entry) {
        event.preventDefault()
        select(entry)
      }
    }
  }

  const inputProps = {
    role: 'combobox' as const,
    'aria-expanded': showList,
    'aria-controls': listId,
    'aria-autocomplete': 'list' as const,
    'aria-activedescendant':
      showList && results.length > 0 ? `${listId}-${activeIndex}` : undefined,
    onFocus: () => {
      setOpen(true)
    },
    onKeyDown,
  }

  return (
    <div className="relative" onBlur={onBlur}>
      <div
        className={cn(
          'border-input dark:bg-input/30 flex h-9 w-full items-stretch divide-x rounded-md border transition-[color,box-shadow]',
          'focus-within:border-ring focus-within:ring-ring/50 focus-within:ring-[3px]',
        )}
      >
        <div className="flex w-10 shrink-0 items-center justify-center">
          <McpServerIcon server={selected} url={url} />
        </div>
        <input
          {...inputProps}
          id={`${idPrefix}-name`}
          required
          maxLength={mcpServerNameMaxLength}
          aria-invalid={nameInvalid}
          value={name}
          placeholder="Name"
          autoComplete="off"
          className={cn(inputClassName, 'sm:max-w-56')}
          onFocus={() => {
            setNameFocused(true)
            setOpen(true)
          }}
          onBlur={() => {
            setNameFocused(false)
          }}
          onChange={(event) => {
            setHighlighted(0)
            setOpen(true)
            onChange({ name: event.target.value, url })
          }}
        />
        <input
          id={`${idPrefix}-url`}
          required
          value={url}
          placeholder="https://example.com/mcp"
          autoComplete="off"
          className={inputClassName}
          onFocus={() => {
            setOpen(false)
          }}
          onChange={(event) => {
            onChange({ url: event.target.value })
          }}
        />
      </div>
      {showList && (
        <div
          id={listId}
          role="listbox"
          aria-label="MCP registry results"
          className="bg-popover text-popover-foreground absolute left-0 right-0 top-full z-50 mt-1 max-h-80 overflow-y-auto rounded-md border p-1 shadow-md"
        >
          <div className="text-muted-foreground border-b px-2 py-1.5 text-xs">
            Searching the MCP registry by name, description, and URL. Select a server to fill in its
            endpoint.
          </div>
          {results.length === 0 ? (
            <div className="text-muted-foreground px-2 py-2 text-sm">
              {serversQuery.isError
                ? 'Could not load registry results.'
                : serversQuery.isFetching
                  ? 'Searching…'
                  : 'No registry servers match.'}
            </div>
          ) : (
            results.map(({ key, server, remote }, index) => {
              return (
                <button
                  key={key}
                  type="button"
                  id={`${listId}-${index}`}
                  role="option"
                  aria-selected={index === activeIndex}
                  tabIndex={-1}
                  className={cn(
                    'flex w-full cursor-default items-center gap-3 rounded-sm px-2 py-2 text-left text-sm',
                    index === activeIndex && 'bg-accent text-accent-foreground',
                  )}
                  onMouseDown={(event) => {
                    event.preventDefault()
                  }}
                  onMouseEnter={() => {
                    setHighlighted(index)
                  }}
                  onClick={() => {
                    select({ key, server, remote })
                  }}
                >
                  <McpServerIcon server={server} url={remote.url} className="size-6" />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-baseline gap-2">
                      <span className="truncate font-medium">{registryServerLabel(server)}</span>
                      <span className="text-muted-foreground truncate text-xs">{server.name}</span>
                    </div>
                    {server.description && (
                      <div className="text-muted-foreground line-clamp-1 text-xs">
                        {server.description}
                      </div>
                    )}
                    <div className="text-muted-foreground truncate text-xs">{remote.url}</div>
                  </div>
                </button>
              )
            })
          )}
          {results.length > 0 && serversQuery.hasNextPage && (
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground w-full px-2 py-2 text-left text-xs disabled:opacity-50"
              disabled={serversQuery.isFetchingNextPage}
              onMouseDown={(event) => {
                event.preventDefault()
              }}
              onClick={() => {
                void serversQuery.fetchNextPage()
              }}
            >
              {serversQuery.isFetchingNextPage ? 'Loading more…' : 'Load more results'}
            </button>
          )}
        </div>
      )}
    </div>
  )
}
