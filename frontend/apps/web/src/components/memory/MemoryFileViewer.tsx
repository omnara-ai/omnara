import {
  type MemoryScope,
  useDeleteMemoryFile,
  useMemoryFile,
  useWriteMemoryFile,
} from '@omnara/react'
import { ApiError } from '@omnara/sdk'
import { CatchBoundary } from '@tanstack/react-router'
import { lazy, Suspense, useEffect, useMemo, useRef, useState } from 'react'
import { Streamdown } from 'streamdown'

import { Button } from '@/components/ui/button'
import { Empty, EmptyDescription } from '@/components/ui/empty'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useUnsavedChangesWarning } from '@/hooks/use-unsaved-changes-warning'
import { attachmentSize } from '@/lib/agent-attachments'
import { downloadMemoryBlob, memoryPreview } from '@/lib/memory-files'
import { errorMessage } from '@/lib/submit-status'

const MAX_MARKDOWN_PREVIEW_CHARS = 256 * 1024

const TextFileEditor = lazy(async () => ({
  default: (await import('@/components/ui/text-file-editor')).TextFileEditor,
}))

export function MemoryFileViewer({
  scope,
  path,
  canWrite,
  onDeleted,
}: {
  scope: MemoryScope
  path: string
  canWrite: boolean
  onDeleted: () => void
}) {
  const query = useMemoryFile(scope, path)
  const preview = useMemo(
    () => (query.data ? { ...query.data, ...memoryPreview(query.data.bytes) } : null),
    [query.data],
  )
  return (
    <>
      {query.isError && (
        <div role="alert" className="p-5 text-sm">
          <p>{errorMessage(query.error, 'Could not load file')}</p>
          <Button variant="outline" onClick={() => void query.refetch()}>
            Retry
          </Button>
        </div>
      )}
      {preview ? (
        <FileContent
          scope={scope}
          path={path}
          canWrite={canWrite}
          preview={preview}
          onDeleted={onDeleted}
          onRefresh={() => void query.refetch()}
        />
      ) : (
        !query.isError && <p className="text-muted-foreground p-5 text-sm">Loading file…</p>
      )}
    </>
  )
}

function FileContent({
  scope,
  path,
  canWrite,
  preview,
  onDeleted,
  onRefresh,
}: {
  scope: MemoryScope
  path: string
  canWrite: boolean
  preview: {
    text: string | null
    type: string | null
    digest: string
    bytes: Uint8Array<ArrayBuffer>
  }
  onDeleted: () => void
  onRefresh: () => void
}) {
  const [baseline, setBaseline] = useState({ text: preview.text, digest: preview.digest })
  const [draft, setDraft] = useState(preview.text ?? '')
  const write = useWriteMemoryFile(scope)
  const remove = useDeleteMemoryFile(scope)
  const dirty = baseline.text !== null && draft !== baseline.text
  useUnsavedChangesWarning(
    dirty,
    ({ current, next }) =>
      current.pathname === next.pathname &&
      ('path' in current.search ? current.search.path : undefined) ===
        ('path' in next.search ? next.search.path : undefined),
  )
  const pending = write.isPending || remove.isPending
  const changed = !write.isPending && baseline.digest !== preview.digest
  const error = write.error ?? remove.error
  const currentDigest =
    write.error instanceof ApiError && write.error.code === 'file_content_conflict'
      ? write.error.currentDigest
      : undefined
  function save() {
    write.mutate(
      { path, content: new Blob([draft]), expectedDigest: currentDigest ?? baseline.digest },
      {
        onSuccess: (result) => {
          setBaseline({ text: draft, digest: result.digest })
        },
      },
    )
  }
  function deleteFile() {
    if (!window.confirm(`Delete ${path}?${dirty ? ' Unsaved edits will be lost.' : ''}`)) return
    remove.mutate({ path, digest: baseline.digest }, { onSuccess: onDeleted })
  }

  return (
    <div className="flex min-w-0 flex-col gap-4 p-4 sm:p-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="break-all font-medium">{path}</h2>
          <p className="text-muted-foreground text-xs">
            {attachmentSize(preview.bytes.byteLength)}
            {dirty ? ' · Unsaved changes' : ''}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          {canWrite && baseline.text !== null && (
            <Button size="sm" loading={write.isPending} disabled={!dirty || pending} onClick={save}>
              {currentDigest ? 'Replace current contents' : 'Save changes'}
            </Button>
          )}
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              downloadMemoryBlob(new Blob([preview.bytes]), path)
            }}
          >
            Download
          </Button>
          {canWrite && (
            <Button
              size="sm"
              variant="ghost"
              className="text-destructive hover:text-destructive"
              disabled={pending}
              loading={remove.isPending}
              onClick={deleteFile}
            >
              Delete
            </Button>
          )}
        </div>
      </div>
      <FileNotices
        changed={changed}
        currentDigest={currentDigest}
        error={error}
        pending={pending}
        onRefresh={onRefresh}
        onLoadLatest={() => {
          if (!dirty || window.confirm('Discard your edits and reload the latest file?')) {
            setBaseline({ text: preview.text, digest: preview.digest })
            setDraft(preview.text ?? '')
            write.reset()
            remove.reset()
          }
        }}
      />
      {baseline.text !== null ? (
        <TextPreview
          scope={scope}
          path={path}
          draft={draft}
          setDraft={setDraft}
          canWrite={canWrite}
          pending={pending}
        />
      ) : (
        <BinaryPreview bytes={preview.bytes} type={preview.type} path={path} />
      )}
    </div>
  )
}

function TextPreview({
  scope,
  path,
  draft,
  setDraft,
  canWrite,
  pending,
}: {
  scope: MemoryScope
  path: string
  draft: string
  setDraft: (value: string) => void
  canWrite: boolean
  pending: boolean
}) {
  const markdown = path.toLowerCase().endsWith('.md')
  const canPreviewMarkdown = draft.length <= MAX_MARKDOWN_PREVIEW_CHARS
  const editor = (
    <CatchBoundary
      getResetKey={() => path}
      errorComponent={() => (
        <p role="alert">Could not load the editor. Download the file to view it.</p>
      )}
    >
      <Suspense fallback={<p className="text-muted-foreground text-sm">Loading editor…</p>}>
        <TextFileEditor
          id={`memory-${scope.memoryStoreID}-${path}`}
          filename={path}
          value={draft}
          onChange={setDraft}
          readOnly={!canWrite || pending}
          className="h-[min(65vh,48rem)]"
        />
      </Suspense>
    </CatchBoundary>
  )

  return markdown ? (
    <Tabs defaultValue={canPreviewMarkdown ? 'preview' : 'source'} className="gap-4">
      <TabsList aria-label="File view">
        <TabsTrigger value="preview">Preview</TabsTrigger>
        <TabsTrigger value="source">{canWrite ? 'Edit' : 'Source'}</TabsTrigger>
      </TabsList>
      <TabsContent value="source">{editor}</TabsContent>
      <TabsContent value="preview">
        {canPreviewMarkdown ? (
          <Streamdown mode="static" className="min-h-64 overflow-auto text-sm">
            {draft}
          </Streamdown>
        ) : (
          <p className="text-muted-foreground text-sm">
            Markdown preview is unavailable for large files.
          </p>
        )}
      </TabsContent>
    </Tabs>
  ) : (
    editor
  )
}

function FileNotices({
  changed,
  currentDigest,
  error,
  pending,
  onRefresh,
  onLoadLatest,
}: {
  changed: boolean
  currentDigest: string | undefined
  error: Error | null
  pending: boolean
  onRefresh: () => void
  onLoadLatest: () => void
}) {
  return (
    <>
      {changed && (
        <div className="bg-muted rounded-md p-3 text-sm">
          This file changed since you opened it. Your draft is preserved.{' '}
          <button
            className="underline disabled:opacity-50"
            disabled={pending}
            onClick={onLoadLatest}
          >
            Load latest
          </button>
        </div>
      )}
      {error && !(changed && currentDigest) && (
        <p role="alert" className="text-destructive text-sm">
          {currentDigest ? 'This file changed. Your draft is preserved.' : error.message}{' '}
          <button className="underline" onClick={onRefresh}>
            Check latest
          </button>
        </p>
      )}
    </>
  )
}

function BinaryPreview({
  bytes,
  type,
  path,
}: {
  bytes: Uint8Array<ArrayBuffer>
  type: string | null
  path: string
}) {
  const frame = useRef<HTMLIFrameElement>(null)
  const image = useRef<HTMLImageElement>(null)
  useEffect(() => {
    const next = type ? URL.createObjectURL(new Blob([bytes], { type })) : null
    const element = frame.current ?? image.current
    if (element && next) element.src = next
    return () => {
      if (next) URL.revokeObjectURL(next)
    }
  }, [bytes, type])
  if (!type)
    return (
      <Empty className="min-h-64 border">
        <EmptyDescription>
          Preview isn’t available for this file. Download it to open it.
        </EmptyDescription>
      </Empty>
    )
  if (type === 'application/pdf')
    return <iframe ref={frame} title={path} className="h-[65vh] w-full rounded-md border" />
  return (
    <img
      ref={image}
      alt={path}
      className="max-h-[65vh] max-w-full self-center rounded-md object-contain"
    />
  )
}
