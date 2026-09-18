import { type MemoryScope, useMemoryFiles, useMemoryStore } from '@omnara/react'
import { ApiError } from '@omnara/sdk'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { type ReactNode, useState } from 'react'

import {
  ChevronDown,
  ChevronRight,
  ChevronsDownUpIcon,
  ChevronsUpDownIcon,
  CodeBracketIcon,
  DocumentTextIcon,
  File,
  FileArchive,
  FilmIcon,
  Folder,
  MusicalNoteIcon,
  PhotoIcon,
  PlusIcon,
  PresentationChartBarIcon,
  SettingsIcon,
  TableCellsIcon,
} from '@/components/icons'
import { MemoryFileDialog } from '@/components/memory/MemoryFileDialog'
import { MemoryFileViewer } from '@/components/memory/MemoryFileViewer'
import { MemoryStoreDialog } from '@/components/memory/MemoryStoreDialog'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import { Empty, EmptyDescription } from '@/components/ui/empty'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { attachmentSize } from '@/lib/agent-attachments'
import { errorMessage } from '@/lib/submit-status'

export function MemoryStorePage() {
  const { storeId } = useParams({
    from: '/authenticated/onboarded/projects/$projectId/memory/$storeId',
  })
  return (
    <ProjectPageFrame title="Memory">
      {({ activeOrg, projectId, project }) =>
        project?.access.can_read ? (
          <StoreBrowser
            key={storeId}
            scope={{ orgID: activeOrg.id, projectID: projectId, memoryStoreID: storeId }}
            canManage={project.access.can_manage}
          />
        ) : (
          <p className="text-muted-foreground text-sm">
            You don’t have permission to view this store.
          </p>
        )
      }
    </ProjectPageFrame>
  )
}

function StoreBrowser({ scope, canManage }: { scope: MemoryScope; canManage: boolean }) {
  const query = useMemoryStore(scope)
  const navigate = useNavigate()
  const search = useSearch({ from: '/authenticated/onboarded/projects/$projectId/memory/$storeId' })
  const selected = search.path ?? ''
  const folder =
    search.folder ?? (selected.includes('/') ? selected.slice(0, selected.lastIndexOf('/')) : '')
  const [dialog, setDialog] = useState<'settings' | 'file' | null>(null)
  function select(path?: string, directory = folder) {
    void navigate({
      to: '/projects/$projectId/memory/$storeId',
      params: { projectId: scope.projectID, storeId: scope.memoryStoreID },
      search: { path, folder: directory || undefined },
    })
  }
  if (!query.data)
    return (
      <div className="text-sm">
        {query.isError ? (
          <p role="alert">
            {errorMessage(query.error, 'Could not load store')}{' '}
            <button className="underline" onClick={() => void query.refetch()}>
              Retry
            </button>
          </p>
        ) : (
          'Loading store…'
        )}
      </div>
    )
  const store = query.data
  return (
    <div className="flex min-w-0 flex-col gap-4">
      <Link
        to="/projects/$projectId/memory"
        params={{ projectId: scope.projectID }}
        className="text-muted-foreground w-fit text-sm hover:underline"
      >
        ← All stores
      </Link>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <h1 className="type-title break-words">{store.name}</h1>
          <p className="text-muted-foreground mt-1 line-clamp-2 max-w-2xl break-words text-sm">
            {store.description || 'Files shared with agents in this project.'}
          </p>
          {store.read_only && (
            <p className="text-muted-foreground mt-2 text-xs">Read-only for agents</p>
          )}
        </div>
        <div className="flex gap-2">
          {canManage && (
            <Button
              size="sm"
              onClick={() => {
                setDialog('file')
              }}
            >
              <PlusIcon />
              Add file
            </Button>
          )}
          {canManage && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setDialog('settings')
              }}
            >
              <SettingsIcon />
              Settings
            </Button>
          )}
        </div>
      </div>
      <div className="bg-card grid min-h-[32rem] min-w-0 overflow-hidden rounded-lg border md:grid-cols-[16rem_minmax(0,1fr)]">
        <DirectoryBrowser scope={scope} folder={folder} selected={selected} onSelect={select} />
        <div className="min-w-0">
          {selected ? (
            <MemoryFileViewer
              key={selected}
              scope={scope}
              path={selected}
              canWrite={canManage}
              onDeleted={() =>
                void navigate({
                  to: '/projects/$projectId/memory/$storeId',
                  params: { projectId: scope.projectID, storeId: scope.memoryStoreID },
                  search: {},
                  ignoreBlocker: true,
                })
              }
            />
          ) : (
            <Empty className="min-h-64">
              <EmptyDescription>Select a file to preview.</EmptyDescription>
            </Empty>
          )}
        </div>
      </div>
      {dialog === 'settings' && (
        <MemoryStoreDialog
          orgId={scope.orgID}
          projectId={scope.projectID}
          store={store}
          onClose={() => {
            setDialog(null)
          }}
        />
      )}
      {dialog === 'file' && (
        <MemoryFileDialog
          scope={scope}
          folder={folder}
          onClose={() => {
            setDialog(null)
          }}
          onSaved={(path) => {
            select(path, path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '')
          }}
        />
      )}
    </div>
  )
}

function DirectoryBrowser({
  scope,
  folder,
  selected,
  onSelect,
}: {
  scope: MemoryScope
  folder: string
  selected: string
  onSelect: (path?: string, folder?: string) => void
}) {
  const [expansion, setExpansion] = useState<{ all?: boolean; paths: Map<string, boolean> }>({
    paths: new Map(),
  })
  const isExpanded = (path: string): boolean =>
    expansion.paths.get(path) ?? expansion.all ?? (folder === path || folder.startsWith(`${path}/`))
  const canCollapse =
    expansion.all === true ||
    [...expansion.paths.values()].some(Boolean) ||
    (folder !== '' &&
      folder.split('/').some((_, index, parts) => isExpanded(parts.slice(0, index + 1).join('/'))))
  return (
    <aside
      aria-label="Store files"
      className="bg-muted/20 max-h-[35vh] overflow-y-auto border-b md:max-h-[75vh] md:border-b-0 md:border-r"
    >
      <DirectoryEntries
        folderToggle={
          <Button
            size="icon"
            variant="ghost"
            className="size-8"
            aria-label={canCollapse ? 'Collapse all folders' : 'Expand all folders'}
            title={canCollapse ? 'Collapse all folders' : 'Expand all folders'}
            onClick={() => {
              setExpansion({ all: !canCollapse, paths: new Map() })
            }}
          >
            {canCollapse ? (
              <ChevronsDownUpIcon strokeWidth={1.5} />
            ) : (
              <ChevronsUpDownIcon strokeWidth={1.5} />
            )}
          </Button>
        }
        scope={scope}
        folder=""
        selected={selected}
        onSelect={onSelect}
        isExpanded={isExpanded}
        onToggle={(path) => {
          setExpansion((current) => ({
            ...current,
            paths: new Map(current.paths).set(path, !isExpanded(path)),
          }))
        }}
      />
    </aside>
  )
}

function DirectoryEntries({
  folderToggle,
  scope,
  folder,
  selected,
  onSelect,
  isExpanded,
  onToggle,
}: {
  scope: MemoryScope
  folder: string
  selected: string
  onSelect: (path?: string, folder?: string) => void
  isExpanded: (path: string) => boolean
  onToggle: (path: string) => void
  folderToggle?: ReactNode
}) {
  const query = useMemoryFiles(scope, folder)
  const files = useInfiniteQueryItems(query)
  if (folder && query.error instanceof ApiError && query.error.status === 404) return null
  return (
    <>
      {folder === '' && (
        <div className="flex min-h-12 items-center justify-between border-b px-3 py-2">
          <span className="text-muted-foreground text-sm">Contents</span>
          {files.some((file) => file.type === 'directory') && folderToggle}
        </div>
      )}
      <div className={folder === '' ? 'p-2' : undefined}>
        {query.isPending && <p className="text-muted-foreground p-3 text-sm">Loading files…</p>}
        {query.isError && (
          <p role="alert" className="text-destructive p-3 text-sm">
            {errorMessage(query.error, 'Could not list files')}
          </p>
        )}
        {query.isSuccess && files.length === 0 && (
          <p className="text-muted-foreground p-4 text-sm">This folder is empty.</p>
        )}
        <ul>
          {files.map((file) => (
            <li key={file.path}>
              <button
                aria-current={selected === file.path ? 'true' : undefined}
                aria-expanded={file.type === 'directory' ? isExpanded(file.path) : undefined}
                title={file.path}
                className={`flex w-full items-center gap-2 rounded-lg px-2 py-2 text-left text-sm ${selected === file.path ? 'bg-muted' : 'hover:bg-muted/60'}`}
                onClick={() => {
                  if (file.type === 'directory') onToggle(file.path)
                  else onSelect(file.path, folder)
                }}
              >
                {file.type === 'directory' ? (
                  <>
                    {isExpanded(file.path) ? (
                      <ChevronDown className="size-4 shrink-0" />
                    ) : (
                      <ChevronRight className="size-4 shrink-0" />
                    )}
                    <Folder className="size-4 shrink-0" />
                  </>
                ) : (
                  <MemoryFileIcon path={file.path} />
                )}
                <span className="min-w-0 flex-1 truncate">{file.path.split('/').at(-1)}</span>
                {file.type === 'file' && file.size_bytes != null && (
                  <span className="text-muted-foreground shrink-0 text-xs">
                    {attachmentSize(file.size_bytes)}
                  </span>
                )}
              </button>
              {file.type === 'directory' && isExpanded(file.path) && (
                <div className="pl-4">
                  <DirectoryEntries
                    scope={scope}
                    folder={file.path}
                    selected={selected}
                    onSelect={onSelect}
                    isExpanded={isExpanded}
                    onToggle={onToggle}
                  />
                </div>
              )}
            </li>
          ))}
        </ul>
        {query.hasNextPage && (
          <Button
            className="m-3"
            size="sm"
            variant="outline"
            disabled={query.isFetchingNextPage}
            onClick={() => void query.fetchNextPage()}
          >
            Load more
          </Button>
        )}
      </div>
    </>
  )
}

const fileTypeIcons = [
  [/\.(png|jpe?g|gif|webp|svg|ico|bmp|tiff?|avif|heic)$/i, PhotoIcon],
  [/\.(mp4|mov|webm|avi|mkv)$/i, FilmIcon],
  [/\.(mp3|wav|ogg|flac|m4a|aac)$/i, MusicalNoteIcon],
  [/\.(zip|gz|tar|bz2|xz|7z|rar)$/i, FileArchive],
  [/\.(csv|tsv|xlsx?|ods)$/i, TableCellsIcon],
  [/\.(pptx?|odp)$/i, PresentationChartBarIcon],
  [
    /\.(js|jsx|ts|tsx|py|go|rs|java|c|h|cpp|cs|rb|php|sh|html?|css|scss|json|ya?ml|toml|xml|sql)$/i,
    CodeBracketIcon,
  ],
  [/\.(txt|md|mdx|rst|log|pdf|docx?|odt|rtf)$/i, DocumentTextIcon],
] as const

function MemoryFileIcon({ path }: { path: string }) {
  const Icon = fileTypeIcons.find(([pattern]) => pattern.test(path))?.[1] ?? File
  return <Icon className="text-muted-foreground ml-6 size-4 shrink-0" />
}
