import { useRef } from 'react'

import { File as FileIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

export function FilePicker({
  id,
  file,
  accept,
  label = 'File',
  icon: Icon = FileIcon,
  disabled = false,
  onSelect,
}: {
  id: string
  file: File | undefined
  accept?: string
  label?: string
  icon?: typeof FileIcon
  disabled?: boolean
  onSelect: (file: File | undefined) => void
}) {
  const inputRef = useRef<HTMLInputElement | null>(null)

  return (
    <div className="border-border bg-muted/20 flex items-center gap-3 rounded-lg border border-dashed p-4">
      <div className="bg-background flex size-9 shrink-0 items-center justify-center rounded-md border">
        <Icon className="text-muted-foreground size-4" />
      </div>
      <p className={cn('min-w-0 flex-1 truncate text-sm', !file && 'text-muted-foreground')}>
        {file ? file.name : 'No file selected'}
      </p>
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="shrink-0"
        disabled={disabled}
        onClick={() => {
          inputRef.current?.click()
        }}
      >
        Choose file
      </Button>
      <input
        ref={inputRef}
        id={id}
        aria-label={label}
        type="file"
        accept={accept}
        className="sr-only"
        disabled={disabled}
        onChange={(event) => {
          onSelect(event.target.files?.[0])
        }}
      />
    </div>
  )
}
