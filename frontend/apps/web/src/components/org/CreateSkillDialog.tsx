import { useCreateSkill } from '@omnara/react'
import type { SkillOwnerInput } from '@omnara/sdk'
import { FileArchive } from 'lucide-react'
import { type SyntheticEvent, useId, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/lib/submit-status'

export function CreateSkillDialog({
  open,
  onOpenChange,
  orgId,
  owner,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  owner: SkillOwnerInput
}) {
  const inputId = useId()
  const createSkill = useCreateSkill(orgId)
  const [archive, setArchive] = useState<File>()
  const uploadError = createSkill.isError
    ? errorMessage(createSkill.error, 'Could not upload skill')
    : null

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) {
      setArchive(undefined)
      createSkill.reset()
    }
    onOpenChange(nextOpen)
  }

  function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!archive || createSkill.isPending) return
    createSkill.mutate(
      { owner, archive },
      {
        onSuccess: () => {
          handleOpenChange(false)
        },
      },
    )
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Upload skill</DialogTitle>
          <DialogDescription>
            Upload a .zip or .tar.gz archive containing a SKILL.md file. Uploading the same skill
            name creates a new revision.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            submit(event)
          }}
        >
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor={inputId}>Skill archive</FieldLabel>
              <div className="border-border bg-muted/20 flex items-center gap-3 rounded-lg border border-dashed p-4">
                <div className="bg-background flex size-9 shrink-0 items-center justify-center rounded-md border">
                  <FileArchive className="text-muted-foreground size-4" />
                </div>
                <Input
                  id={inputId}
                  type="file"
                  accept=".zip,.tar.gz,application/zip,application/gzip"
                  disabled={createSkill.isPending}
                  onChange={(event) => {
                    setArchive(event.target.files?.[0])
                    createSkill.reset()
                  }}
                />
              </div>
              <FieldDescription>
                The skill name and description are read from SKILL.md.
              </FieldDescription>
            </Field>
            {uploadError && (
              <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
                {uploadError}
              </p>
            )}
            <DialogFooter>
              <Button type="submit" disabled={!archive || createSkill.isPending}>
                {createSkill.isPending && <Spinner />}
                Upload skill
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
