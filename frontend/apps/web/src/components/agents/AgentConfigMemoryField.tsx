import { useMemoryStores } from '@omnara/react'
import type { MemoryStore } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'

import type { BasicMemoryStore } from '@/components/agents/agentConfigBasicExtract'
import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import { PlusIcon, Trash2Icon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { exactNameGlob, useTypeaheadSearch } from '@/hooks/use-resource-list'

const StoreCombobox = createResourceCombobox<MemoryStore>({
  itemKey: (store) => store.name,
  itemLabel: (store) => store.name,
  renderItem: (store) => (
    <span className="flex min-w-0 flex-col gap-0.5">
      <span className="font-medium">
        {store.name}
        {store.read_only ? ' · Read-only' : ''}
      </span>
      <span className="text-muted-foreground line-clamp-2 text-xs">{store.description}</span>
    </span>
  ),
  placeholder: 'Search memory stores…',
  emptyMessage: 'No memory stores found.',
})

export function AgentConfigMemoryField({
  orgId,
  projectId,
  stores,
  onChange,
}: {
  orgId: string
  projectId: string
  stores: BasicMemoryStore[]
  onChange: (stores: BasicMemoryStore[]) => void
}) {
  const [adding, setAdding] = useState(false)
  const search = useTypeaheadSearch()
  const query = useMemoryStores(orgId, projectId, {
    filters: { name: search.name },
    enabled: adding,
  })
  const items = useInfiniteQueryItems(query).filter(
    (store) => !stores.some((attached) => attached.name === store.name),
  )
  return (
    <AgentConfigSectionCard
      title="Memory"
      action={
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="text-muted-foreground size-10 sm:size-8"
          aria-label="Attach memory store"
          onClick={() => {
            setAdding(!adding)
          }}
        >
          <PlusIcon />
        </Button>
      }
    >
      {adding || stores.length > 0 ? (
        <div className="space-y-2 px-5 py-4">
          {stores.map((attachment) => (
            <MemoryAttachment
              key={attachment.name}
              orgId={orgId}
              projectId={projectId}
              attachment={attachment}
              onChange={(access) => {
                onChange(
                  stores.map((store) =>
                    store.name === attachment.name ? { ...store, access } : store,
                  ),
                )
              }}
              onRemove={() => {
                onChange(stores.filter((store) => store.name !== attachment.name))
              }}
            />
          ))}
          {adding && (
            <StoreCombobox
              items={items}
              value={null}
              search={search}
              query={query}
              onValueChange={(store) => {
                if (store) {
                  onChange([
                    ...stores,
                    { name: store.name, access: store.read_only ? 'read_only' : 'read_write' },
                  ])
                  setAdding(false)
                }
              }}
            />
          )}
        </div>
      ) : null}
    </AgentConfigSectionCard>
  )
}

function MemoryAttachment({
  orgId,
  projectId,
  attachment,
  onChange,
  onRemove,
}: {
  orgId: string
  projectId: string
  attachment: BasicMemoryStore
  onChange: (access: BasicMemoryStore['access']) => void
  onRemove: () => void
}) {
  const query = useMemoryStores(orgId, projectId, {
    filters: { name: exactNameGlob(attachment.name) },
  })
  const store = useInfiniteQueryItems(query).find((item) => item.name === attachment.name)
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-md border p-3">
      <div className="min-w-0 flex-1">
        {store ? (
          <Link
            className="text-sm font-medium hover:underline"
            to="/projects/$projectId/memory/$storeId"
            params={{ projectId, storeId: store.id }}
          >
            {store.name}
          </Link>
        ) : (
          <p className="text-sm font-medium">{attachment.name}</p>
        )}
        <p className="text-muted-foreground text-xs">
          {store
            ? store.description
            : query.isPending
              ? 'Loading store…'
              : query.isError
                ? 'Could not load store.'
                : 'Store is no longer available.'}
        </p>
        {store?.read_only && (
          <p className="text-muted-foreground mt-1 text-xs">
            This store is read-only for agents, regardless of attachment access.
          </p>
        )}
        {query.isError && (
          <button type="button" className="text-xs underline" onClick={() => void query.refetch()}>
            Retry
          </button>
        )}
      </div>
      <Select
        value={attachment.access}
        onValueChange={(value) => {
          if (value === 'read_only' || value === 'read_write') onChange(value)
        }}
      >
        <SelectTrigger aria-label={`Access to ${attachment.name}`} className="w-36">
          <SelectValue>
            {attachment.access === 'read_write' ? 'Read & write' : 'Read-only'}
          </SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="read_write">Read & write</SelectItem>
          <SelectItem value="read_only">Read-only</SelectItem>
        </SelectContent>
      </Select>
      <Button
        type="button"
        size="icon"
        variant="ghost"
        aria-label={`Detach ${attachment.name}`}
        onClick={onRemove}
      >
        <Trash2Icon />
      </Button>
    </div>
  )
}
