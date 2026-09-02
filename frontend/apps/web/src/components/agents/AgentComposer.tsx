import type { UseAgentChatResult } from '@omnara/react'
import type { AgentConfigModel } from '@omnara/sdk'
import { useMessageScroller } from '@shadcn/react/message-scroller'
import {
  type ChangeEvent,
  type KeyboardEvent,
  type SyntheticEvent,
  useEffect,
  useEffectEvent,
  useRef,
  useState,
} from 'react'

import { File, FilePlus, SendHorizontal, Square, Upload, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import {
  attachmentSize,
  maxAttachmentCount,
  maxTotalAttachmentBytes,
  selectAgentAttachment,
  type SelectedAgentAttachment,
} from '@/lib/agent-attachments'

function SelectedAttachment({
  attachment,
  disabled,
  onRemove,
}: {
  attachment: SelectedAgentAttachment
  disabled: boolean
  onRemove: () => void
}) {
  const previewURL =
    attachment.kind === 'image'
      ? `data:${attachment.mediaType};base64,${attachment.data}`
      : undefined

  return (
    <div className="bg-muted/40 flex w-56 max-w-full shrink-0 items-center gap-2 rounded-lg border p-1 pr-1.5">
      {previewURL == null ? (
        <span className="bg-background flex size-9 shrink-0 items-center justify-center rounded-md border">
          <File className="text-muted-foreground size-4.5" />
        </span>
      ) : (
        <img src={previewURL} alt="" className="size-9 shrink-0 rounded-md border object-cover" />
      )}
      <span className="min-w-0 flex-1">
        <span className="block truncate text-xs font-medium">{attachment.file.name}</span>
        <span className="text-muted-foreground block text-[11px]">
          {attachmentSize(attachment.file.size)}
        </span>
      </span>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="shrink-0 rounded-full sm:size-6"
        aria-label={`Remove ${attachment.file.name}`}
        disabled={disabled}
        onClick={onRemove}
      >
        <X className="size-3.5" />
      </Button>
    </div>
  )
}

export function AgentComposer({
  chat,
  model,
  onCancel,
  cancelPending,
  cancelError,
  canOperate,
}: {
  chat: UseAgentChatResult
  model?: AgentConfigModel
  onCancel: () => Promise<void>
  cancelPending: boolean
  cancelError?: Error | null
  canOperate: boolean
}) {
  const { scrollToEnd } = useMessageScroller()
  const [text, setText] = useState('')
  const [attachments, setAttachments] = useState<SelectedAgentAttachment[]>([])
  const [attachmentError, setAttachmentError] = useState<string>()
  const [addingFiles, setAddingFiles] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [dragging, setDragging] = useState(false)
  const busyRef = useRef(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const working = chat.isWorking
  const ready = canOperate && chat.historyStatus === 'success'
  const busy = addingFiles || submitting
  const canSend = ready && !busy && (text.trim() !== '' || attachments.length > 0)

  async function send() {
    if (!canSend || busyRef.current) return
    const content = text.trim()
    const selected = attachments
    busyRef.current = true
    setSubmitting(true)
    setAttachmentError(undefined)
    setText('')
    setAttachments([])
    try {
      scrollToEnd()
      await chat.sendMessage({
        text: content,
        attachments: selected.map((attachment) => ({
          data: attachment.data,
          mediaType: attachment.mediaType,
          filename: attachment.file.name,
          sizeBytes: attachment.file.size,
        })),
      })
    } catch {
      setText(content)
      setAttachments(selected)
    }
    busyRef.current = false
    setSubmitting(false)
  }

  async function addFiles(files: FileList | readonly File[] | null) {
    if (files == null || files.length === 0 || model == null || busyRef.current) return
    if (attachments.length + files.length > maxAttachmentCount) {
      setAttachmentError(`You can attach up to ${String(maxAttachmentCount)} files.`)
      return
    }
    const selectedFiles = Array.from(files)
    const totalBytes = [...attachments.map(({ file }) => file), ...selectedFiles].reduce(
      (sum, file) => sum + file.size,
      0,
    )
    if (totalBytes > maxTotalAttachmentBytes) {
      setAttachmentError('Attachments exceed the 24 MB combined limit.')
      return
    }

    busyRef.current = true
    setAddingFiles(true)
    setAttachmentError(undefined)
    try {
      const selected = await Promise.all(
        selectedFiles.map((file) => selectAgentAttachment(file, model)),
      )
      setAttachments((current) => [...current, ...selected])
    } catch (error) {
      setAttachmentError(error instanceof Error ? error.message : 'Could not attach the file.')
    }
    busyRef.current = false
    setAddingFiles(false)
  }

  const addDroppedFiles = useEffectEvent((files: FileList | null) => {
    void addFiles(files)
  })

  useEffect(() => {
    const acceptsFiles = ready && model != null && !busy

    function hasFiles(event: DragEvent) {
      return Array.from(event.dataTransfer?.types ?? []).includes('Files')
    }

    function onDragOver(event: DragEvent) {
      if (!hasFiles(event)) return
      event.preventDefault()
      if (event.dataTransfer != null) event.dataTransfer.dropEffect = acceptsFiles ? 'copy' : 'none'
      if (acceptsFiles) setDragging(true)
    }

    function onDragLeave(event: DragEvent) {
      if (event.relatedTarget == null) setDragging(false)
    }

    function onDrop(event: DragEvent) {
      if (!hasFiles(event)) return
      event.preventDefault()
      setDragging(false)
      if (acceptsFiles) addDroppedFiles(event.dataTransfer?.files ?? null)
    }

    window.addEventListener('dragover', onDragOver)
    window.addEventListener('dragleave', onDragLeave)
    window.addEventListener('drop', onDrop)
    return () => {
      window.removeEventListener('dragover', onDragOver)
      window.removeEventListener('dragleave', onDragLeave)
      window.removeEventListener('drop', onDrop)
    }
  }, [busy, model, ready])

  function onFileChange(event: ChangeEvent<HTMLInputElement>) {
    void addFiles(event.target.files)
    event.target.value = ''
  }

  function onSubmit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    void send()
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault()
      void send()
    }
  }

  return (
    <form onSubmit={onSubmit} className="bg-background relative rounded-2xl border p-2 shadow-sm">
      {dragging && (
        <div className="bg-background/95 border-primary pointer-events-none absolute inset-0 z-10 flex items-center justify-center gap-2 rounded-2xl border-2 text-sm font-medium shadow-sm">
          <Upload className="size-5" /> Drop files to attach
        </div>
      )}
      {attachments.length > 0 && (
        <div className="flex gap-2 overflow-x-auto px-1 pb-2">
          {attachments.map((attachment) => (
            <SelectedAttachment
              key={attachment.id}
              attachment={attachment}
              disabled={busy}
              onRemove={() => {
                if (busyRef.current) return
                setAttachments((current) => current.filter(({ id }) => id !== attachment.id))
              }}
            />
          ))}
        </div>
      )}
      {chat.error && <p className="text-destructive px-2 pb-2 text-xs">{chat.error.message}</p>}
      {cancelError && <p className="text-destructive px-2 pb-2 text-xs">{cancelError.message}</p>}
      {attachmentError && (
        <p role="alert" className="text-destructive break-words px-2 pb-2 text-xs">
          {attachmentError}
        </p>
      )}
      <div className="flex items-end gap-1">
        <input
          ref={inputRef}
          type="file"
          multiple
          hidden
          disabled={!ready || model == null || busy}
          onChange={onFileChange}
        />
        <Button
          type="button"
          variant="secondary"
          size="icon"
          className="shrink-0 rounded-full"
          aria-label="Add files"
          title="Add files"
          disabled={!ready || model == null || busy}
          loading={addingFiles}
          icon={<FilePlus className="size-4.5" />}
          onClick={() => inputRef.current?.click()}
        />
        <Textarea
          value={text}
          placeholder={
            !canOperate
              ? 'You don’t have permission to message this agent'
              : chat.historyStatus === 'pending'
                ? 'Loading conversation…'
                : 'Message the agent…'
          }
          className="max-h-40 min-h-9 min-w-0 flex-1 resize-none border-0 bg-transparent px-2 py-2 shadow-none focus-visible:ring-0 dark:bg-transparent"
          disabled={!ready}
          readOnly={submitting}
          onChange={(event) => {
            setText(event.target.value)
          }}
          onKeyDown={onKeyDown}
          onPaste={(event) => {
            const files = event.clipboardData.files
            if (files.length === 0 || !ready || model == null || busyRef.current) return
            event.preventDefault()
            void addFiles(files)
          }}
        />
        <div className="flex items-center gap-1">
          {working && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="size-10 rounded-full px-0 sm:h-8 sm:w-auto sm:rounded-md sm:px-2.5 [&_[data-slot=button-label]]:hidden sm:[&_[data-slot=button-label]]:inline"
              aria-label="Stop agent"
              disabled={!canOperate || cancelPending}
              loading={cancelPending}
              icon={<Square className="size-3 fill-current" />}
              onClick={() => void onCancel().catch(() => undefined)}
            >
              Stop
            </Button>
          )}
          <Button
            type="submit"
            size="icon"
            className="rounded-full"
            aria-label="Send message"
            title="Send message"
            disabled={!canSend}
            loading={submitting}
            icon={<SendHorizontal className="size-4.5" />}
          />
        </div>
      </div>
    </form>
  )
}
