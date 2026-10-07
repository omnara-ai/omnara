import { useUpdateSkill } from '@omnara/react'
import type { Skill } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

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
import {
  bundleSource,
  readSkillSource,
  type SkillBundle,
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

/** Replace all of a skill's files with an uploaded folder or archive, as a new revision. */
export function UploadSkillVersionDialog({
  onClose,
  orgId,
  skill,
  readSource = bundleSource,
}: {
  onClose: () => void
  orgId: string
  skill: Skill
  readSource?: (source: SkillSource) => Promise<SkillBundle[]>
}) {
  const updateSkill = useUpdateSkill(orgId)
  const pick = useArchivePick(skill.name, readSource)
  const busy = pick.preparing || updateSkill.isPending

  function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy || !pick.archive) return
    updateSkill.mutate(
      { skillID: skill.id, body: { archive: pick.archive } },
      { onSuccess: onClose },
    )
  }

  return (
    <Dialog
      open
      onOpenChange={(nextOpen) => {
        if (!nextOpen && !pick.preparing) onClose()
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Upload new version</DialogTitle>
          <DialogDescription>
            Creates revision v{skill.revision + 1}. The SKILL.md name must stay{' '}
            <span className="font-mono">{skill.name}</span>.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            submit(event)
          }}
        >
          <FieldGroup>
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
            {updateSkill.isError && (
              <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
                {errorMessage(updateSkill.error, 'Could not update skill')}
              </p>
            )}
            <DialogFooter>
              <Button type="submit" disabled={!pick.archive || busy} loading={busy}>
                Upload
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
