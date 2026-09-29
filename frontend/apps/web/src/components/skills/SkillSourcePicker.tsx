import { type DragEvent, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import type { SkillSource } from '@/lib/skill-bundles'

function draggingFiles(event: DragEvent) {
  return Array.from(event.dataTransfer.types).includes('Files')
}

export function SkillSourcePicker({
  selectedName,
  summary,
  busy = false,
  disabled = false,
  onSelect,
}: {
  selectedName: string | undefined
  summary: string | undefined
  busy?: boolean
  disabled?: boolean
  onSelect: (source: SkillSource) => void
}) {
  const filesInputRef = useRef<HTMLInputElement | null>(null)
  const folderInputRef = useRef<HTMLInputElement | null>(null)
  const [dragging, setDragging] = useState(false)

  return (
    <div
      data-dragging={dragging || undefined}
      className="border-border bg-muted/30 data-[dragging]:border-primary data-[dragging]:bg-primary/5 flex flex-col items-center gap-2 rounded-xl border px-6 py-10 text-center transition-colors"
      onDragOver={(event) => {
        if (disabled || !draggingFiles(event)) return
        event.preventDefault()
        event.stopPropagation()
        event.dataTransfer.dropEffect = 'copy'
        setDragging(true)
      }}
      onDragLeave={(event) => {
        if (
          !(
            event.relatedTarget instanceof Node && event.currentTarget.contains(event.relatedTarget)
          )
        )
          setDragging(false)
      }}
      onDrop={(event) => {
        if (disabled || !draggingFiles(event)) return
        event.preventDefault()
        event.stopPropagation()
        setDragging(false)
        const entries = Array.from(event.dataTransfer.items).flatMap((item) => {
          const entry = item.webkitGetAsEntry()
          return entry ? [entry] : []
        })
        const files = Array.from(event.dataTransfer.files)
        if (entries.length > 0) onSelect({ kind: 'drop', entries })
        else if (files.length > 0) onSelect({ kind: 'files', files })
      }}
    >
      <p className="flex items-center gap-2 text-base font-medium">
        {busy && <Spinner className="text-muted-foreground size-4" />}
        {selectedName ?? 'Drop files or a folder here'}
      </p>
      <p className="text-muted-foreground max-w-md text-sm">
        {summary ?? 'A .zip, .tar.gz, or folder containing one or more skills'}
      </p>
      <div className="mt-3 flex flex-wrap justify-center gap-3">
        <Button
          type="button"
          variant="secondary"
          disabled={disabled}
          onClick={() => {
            filesInputRef.current?.click()
          }}
        >
          Choose files
        </Button>
        <Button
          type="button"
          variant="secondary"
          disabled={disabled}
          onClick={() => {
            folderInputRef.current?.click()
          }}
        >
          Choose a folder
        </Button>
      </div>
      <input
        ref={filesInputRef}
        aria-label="Skill files"
        type="file"
        multiple
        accept=".zip,.tar.gz,.tgz,.md,application/zip,application/gzip,text/markdown"
        className="sr-only"
        tabIndex={-1}
        disabled={disabled}
        onChange={(event) => {
          const files = Array.from(event.target.files ?? [])
          event.target.value = ''
          if (files.length > 0) onSelect({ kind: 'files', files })
        }}
      />
      <input
        ref={(element) => {
          folderInputRef.current = element
          if (element) element.webkitdirectory = true
        }}
        aria-label="Skill folder"
        type="file"
        multiple
        className="sr-only"
        tabIndex={-1}
        disabled={disabled}
        onChange={(event) => {
          const files = Array.from(event.target.files ?? [])
          event.target.value = ''
          if (files.length > 0) onSelect({ kind: 'folder', files })
        }}
      />
    </div>
  )
}
