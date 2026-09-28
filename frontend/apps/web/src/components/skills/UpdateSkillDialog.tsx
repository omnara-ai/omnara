import { useSkill, useUpdateSkill } from '@omnara/react'
import type { Skill } from '@omnara/sdk'
import { type SyntheticEvent, useId, useState } from 'react'

import { LazySkillMdEditor } from '@/components/skills/LazySkillMdEditor'
import { SkillArchivePicker } from '@/components/skills/SkillArchivePicker'
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { checkSkillMd } from '@/lib/skill-bundles'
import { errorMessage } from '@/lib/submit-status'

export function UpdateSkillDialog({
  open,
  onOpenChange,
  orgId,
  skill,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  skill: Skill
}) {
  const archiveInputId = useId()
  const updateSkill = useUpdateSkill(orgId)
  const detail = useSkill(orgId, skill.id, open)
  const [tab, setTab] = useState('skill-md')
  const [archive, setArchive] = useState<File>()
  const [draftMd, setDraftMd] = useState<string>()
  const currentMd = detail.data?.skill_md ?? ''
  const editorValue = draftMd ?? currentMd
  const draftCheck = checkSkillMd(editorValue, skill.name)
  const uploadError = updateSkill.isError
    ? errorMessage(updateSkill.error, 'Could not update skill')
    : null

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) {
      setArchive(undefined)
      setDraftMd(undefined)
      setTab('skill-md')
      updateSkill.reset()
    }
    onOpenChange(nextOpen)
  }

  function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (updateSkill.isPending) return
    const body =
      tab === 'skill-md'
        ? draftMd !== undefined && draftMd !== currentMd
          ? { skill_md: draftMd }
          : undefined
        : archive
          ? { archive }
          : undefined
    if (!body) return
    updateSkill.mutate(
      { skillID: skill.id, body },
      {
        onSuccess: () => {
          handleOpenChange(false)
        },
      },
    )
  }

  const canSave =
    tab === 'skill-md'
      ? detail.isSuccess && draftMd !== undefined && draftMd !== currentMd && draftCheck.ok
      : archive !== undefined

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-5xl">
        <DialogHeader>
          <DialogTitle>Edit {skill.name}</DialogTitle>
          <DialogDescription>
            Saving creates revision v{skill.revision + 1}. The SKILL.md name must stay{' '}
            <span className="font-mono">{skill.name}</span>.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            submit(event)
          }}
        >
          <FieldGroup>
            <Tabs
              value={tab}
              onValueChange={(nextTab) => {
                setTab(nextTab)
                updateSkill.reset()
              }}
            >
              <TabsList aria-label="Edit mode">
                <TabsTrigger value="skill-md">SKILL.md</TabsTrigger>
                <TabsTrigger value="archive">Upload archive</TabsTrigger>
              </TabsList>
              <TabsContent value="skill-md">
                <Field>
                  {detail.isPending ? (
                    <p className="text-muted-foreground text-sm">Loading SKILL.md…</p>
                  ) : detail.isError ? (
                    <p className="text-destructive text-sm">Could not load SKILL.md.</p>
                  ) : (
                    <LazySkillMdEditor
                      id={skill.id}
                      className="h-[65vh]"
                      value={editorValue}
                      readOnly={updateSkill.isPending}
                      problem={draftCheck.ok ? undefined : draftCheck.problem}
                      onChange={(nextValue) => {
                        setDraftMd(nextValue)
                        updateSkill.reset()
                      }}
                    />
                  )}
                  <FieldDescription>
                    All other files in the skill are kept unchanged.
                  </FieldDescription>
                </Field>
              </TabsContent>
              <TabsContent value="archive">
                <Field>
                  <FieldLabel htmlFor={archiveInputId}>Skill archive</FieldLabel>
                  <SkillArchivePicker
                    id={archiveInputId}
                    file={archive}
                    disabled={updateSkill.isPending}
                    onSelect={(file) => {
                      setArchive(file)
                      updateSkill.reset()
                    }}
                  />
                  <FieldDescription>
                    A .zip or .tar.gz archive containing SKILL.md replaces all of the skill&rsquo;s
                    files.
                  </FieldDescription>
                </Field>
              </TabsContent>
            </Tabs>
            {uploadError && (
              <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
                {uploadError}
              </p>
            )}
            <DialogFooter>
              <Button
                type="submit"
                disabled={!canSave || updateSkill.isPending}
                loading={updateSkill.isPending}
              >
                Save
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
