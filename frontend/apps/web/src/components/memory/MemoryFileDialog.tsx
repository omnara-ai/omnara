import { MAX_MEMORY_FILE_BYTES, type MemoryScope, useWriteMemoryFile } from '@omnara/react'
import { ApiError } from '@omnara/sdk'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { FilePicker } from '@/components/ui/file-picker'
import { Input } from '@/components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'

export function MemoryFileDialog({
  scope,
  folder,
  onClose,
  onSaved,
}: {
  scope: MemoryScope
  folder: string
  onClose: () => void
  onSaved: (path: string) => void
}) {
  const [mode, setMode] = useState('new')
  const upload = mode === 'upload'
  const pathPrefix = folder ? `${folder}/` : ''
  const [path, setPath] = useState(pathPrefix)
  const [file, setFile] = useState<File>()
  const [text, setText] = useState('')
  const write = useWriteMemoryFile(scope)
  const currentDigest =
    write.error instanceof ApiError && write.error.code === 'file_content_conflict'
      ? write.error.currentDigest
      : undefined
  const size = upload ? (file?.size ?? 0) : 0
  const valid = path !== '' && (!upload || file !== undefined) && size <= MAX_MEMORY_FILE_BYTES
  function save(expectedDigest?: string) {
    const content = upload ? file : new Blob([text])
    if (!content || !valid || write.isPending) return
    write.mutate(
      { path, content, expectedDigest },
      {
        onSuccess: () => {
          onSaved(path)
          onClose()
        },
      },
    )
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !write.isPending) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add file</DialogTitle>
          <DialogDescription>
            Files can be up to 10 MiB. Use a path to place a file inside a folder.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            save(currentDigest)
          }}
        >
          <FieldGroup>
            <Tabs
              value={mode}
              className="gap-4"
              onValueChange={(value) => {
                setMode(value)
                write.reset()
              }}
            >
              <TabsList aria-label="Add file method">
                <TabsTrigger value="new" disabled={write.isPending}>
                  Create text file
                </TabsTrigger>
                <TabsTrigger value="upload" disabled={write.isPending}>
                  Upload file
                </TabsTrigger>
              </TabsList>
              <Field>
                <FieldLabel htmlFor="memory-file-path">Path</FieldLabel>
                <Input
                  id="memory-file-path"
                  required
                  value={path}
                  disabled={write.isPending}
                  placeholder="notes/overview.md"
                  onChange={(event) => {
                    setPath(event.target.value)
                    write.reset()
                  }}
                />
              </Field>
              <TabsContent value="new" forceMount className="data-[state=inactive]:hidden">
                <Field>
                  <FieldLabel htmlFor="memory-file-content">Content</FieldLabel>
                  <Textarea
                    id="memory-file-content"
                    value={text}
                    disabled={write.isPending}
                    onChange={(event) => {
                      setText(event.target.value)
                    }}
                    className="min-h-40 font-mono"
                  />
                </Field>
              </TabsContent>
              <TabsContent value="upload" forceMount className="data-[state=inactive]:hidden">
                <Field>
                  <FieldLabel htmlFor="memory-upload">File</FieldLabel>
                  <FilePicker
                    id="memory-upload"
                    file={file}
                    disabled={write.isPending}
                    onSelect={(selected) => {
                      setFile(selected)
                      if (
                        selected &&
                        (path === '' ||
                          path === pathPrefix ||
                          (file && path === pathPrefix + file.name))
                      ) {
                        setPath(pathPrefix + selected.name)
                      }
                      write.reset()
                    }}
                  />
                </Field>
              </TabsContent>
            </Tabs>
            {size > MAX_MEMORY_FILE_BYTES && (
              <p role="alert" className="text-destructive text-sm">
                The file exceeds the 10 MiB limit.
              </p>
            )}
            {currentDigest ? (
              <p role="alert" className="text-sm">
                A file already exists at this path. Replace its current contents?
              </p>
            ) : (
              write.error && (
                <p role="alert" className="text-destructive text-sm">
                  {write.error.message}
                </p>
              )
            )}
            <DialogFooter>
              <Button type="submit" disabled={!valid || write.isPending} loading={write.isPending}>
                {currentDigest ? 'Replace file' : upload ? 'Upload' : 'Create file'}
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
