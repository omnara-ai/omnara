import { useSkill, useUpdateSkill } from '@omnara/react'
import type { Skill } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { LazySkillMdEditor } from '@/components/skills/LazySkillMdEditor'
import { SkillSourcePicker } from '@/components/skills/SkillSourcePicker'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup } from '@/components/ui/field'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  bundleSource,
  checkSkillMd,
  readSkillSource,
  type SkillBundle,
  type SkillMdProblem,
  type SkillSource,
  skillSourceName,
} from '@/lib/skill-bundles'
import { errorMessage } from '@/lib/submit-status'

type PickedArchive = { ok: true; archive: File } | { ok: false; message: string }

async function pickArchive(
  source: SkillSource,
  skillName: string,
  readSource: (source: SkillSource) => Promise<SkillBundle[]>,
): Promise<PickedArchive> {
  const name = skillSourceName(source)
  const bundled = await readSkillSource(source, readSource)
  if (!bundled.ok) return bundled
  const [only, ...rest] = bundled.bundles
  if (!only) return { ok: false, message: `No SKILL.md found in ${name}.` }
  if (rest.length > 0) {
    return {
      ok: false,
      message: `${name} contains ${bundled.bundles.length} skills. Choose a single skill.`,
    }
  }
  if (only.problem !== undefined) return { ok: false, message: only.problem }
  if (only.label !== skillName) {
    return { ok: false, message: `SKILL.md frontmatter \`name\` must stay \`${skillName}\`.` }
  }
  return { ok: true, archive: only.archive }
}

function archiveBody(archive: File | undefined) {
  return archive ? { archive } : undefined
}

function skillMdBody(draftMd: string | undefined, currentMd: string) {
  return draftMd !== undefined && draftMd !== currentMd ? { skill_md: draftMd } : undefined
}

function useArchivePick(
  skillName: string,
  readSource: (source: SkillSource) => Promise<SkillBundle[]>,
) {
  const [archive, setArchive] = useState<File>()
  const [pickedName, setPickedName] = useState<string>()
  const [sourceError, setSourceError] = useState<string>()
  const [preparing, setPreparing] = useState(false)

  function reset() {
    setArchive(undefined)
    setPickedName(undefined)
    setSourceError(undefined)
  }

  async function select(source: SkillSource) {
    reset()
    setPickedName(skillSourceName(source))
    setPreparing(true)
    const picked = await pickArchive(source, skillName, readSource).finally(() => {
      setPreparing(false)
    })
    if (picked.ok) {
      setArchive(picked.archive)
      return
    }
    setPickedName(undefined)
    setSourceError(picked.message)
  }

  return { archive, pickedName, sourceError, preparing, reset, select }
}

function CurrentSkillMdField({
  skillId,
  loading,
  failed,
  value,
  readOnly,
  problem,
  onChange,
}: {
  skillId: string
  loading: boolean
  failed: boolean
  value: string
  readOnly: boolean
  problem: SkillMdProblem | undefined
  onChange: (value: string) => void
}) {
  return (
    <Field>
      {loading ? (
        <p className="text-muted-foreground text-sm">Loading SKILL.md…</p>
      ) : failed ? (
        <p className="text-destructive text-sm">Could not load SKILL.md.</p>
      ) : (
        <LazySkillMdEditor
          id={skillId}
          className="h-[65vh]"
          value={value}
          readOnly={readOnly}
          problem={problem}
          onChange={onChange}
        />
      )}
      <FieldDescription>All other files in the skill are kept unchanged.</FieldDescription>
    </Field>
  )
}

function SkillUploadField({
  skillName,
  pickedName,
  picked,
  preparing,
  sourceError,
  disabled,
  onSelect,
}: {
  skillName: string
  pickedName: string | undefined
  picked: boolean
  preparing: boolean
  sourceError: string | undefined
  disabled: boolean
  onSelect: (source: SkillSource) => void
}) {
  return (
    <Field>
      <SkillSourcePicker
        selectedName={pickedName}
        summary={picked ? skillName : 'A .zip, .tar.gz, or folder containing SKILL.md'}
        busy={preparing}
        disabled={disabled}
        onSelect={onSelect}
      />
      {sourceError && (
        <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
          {sourceError}
        </p>
      )}
      <FieldDescription>The upload replaces all of the skill&rsquo;s files.</FieldDescription>
    </Field>
  )
}

export function UpdateSkillDialog({
  open,
  onOpenChange,
  orgId,
  skill,
  readSource = bundleSource,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  skill: Skill
  readSource?: (source: SkillSource) => Promise<SkillBundle[]>
}) {
  const updateSkill = useUpdateSkill(orgId)
  const detail = useSkill(orgId, skill.id, open)
  const pick = useArchivePick(skill.name, readSource)
  const [tab, setTab] = useState('skill-md')
  const [draftMd, setDraftMd] = useState<string>()
  const busy = pick.preparing || updateSkill.isPending
  const currentMd = detail.data?.skill_md ?? ''
  const editorValue = draftMd ?? currentMd
  const draftCheck = checkSkillMd(editorValue, skill.name)
  const body = tab === 'upload' ? archiveBody(pick.archive) : skillMdBody(draftMd, currentMd)
  const canSave = tab === 'upload' || (detail.isSuccess && draftCheck.ok)
  const uploadError = updateSkill.isError
    ? errorMessage(updateSkill.error, 'Could not update skill')
    : null

  function handleOpenChange(nextOpen: boolean) {
    if (pick.preparing) return
    if (!nextOpen) {
      pick.reset()
      setDraftMd(undefined)
      setTab('skill-md')
      updateSkill.reset()
    }
    onOpenChange(nextOpen)
  }

  function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy || !body || !canSave) return
    updateSkill.mutate(
      { skillID: skill.id, body },
      {
        onSuccess: () => {
          handleOpenChange(false)
        },
      },
    )
  }

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
                if (pick.preparing) return
                setTab(nextTab)
                updateSkill.reset()
              }}
            >
              <TabsList aria-label="Edit mode">
                <TabsTrigger value="skill-md">SKILL.md</TabsTrigger>
                <TabsTrigger value="upload">Upload</TabsTrigger>
              </TabsList>
              <TabsContent value="skill-md">
                <CurrentSkillMdField
                  skillId={skill.id}
                  loading={detail.isPending}
                  failed={detail.isError}
                  value={editorValue}
                  readOnly={updateSkill.isPending}
                  problem={draftCheck.ok ? undefined : draftCheck.problem}
                  onChange={(nextValue) => {
                    setDraftMd(nextValue)
                    updateSkill.reset()
                  }}
                />
              </TabsContent>
              <TabsContent value="upload">
                <SkillUploadField
                  skillName={skill.name}
                  pickedName={pick.pickedName}
                  picked={pick.archive !== undefined}
                  preparing={pick.preparing}
                  sourceError={pick.sourceError}
                  disabled={busy}
                  onSelect={(source) => {
                    updateSkill.reset()
                    void pick.select(source)
                  }}
                />
              </TabsContent>
            </Tabs>
            {uploadError && (
              <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
                {uploadError}
              </p>
            )}
            <DialogFooter>
              <Button type="submit" disabled={!body || !canSave || busy} loading={busy}>
                Save
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
