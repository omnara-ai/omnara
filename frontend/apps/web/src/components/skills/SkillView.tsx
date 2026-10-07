import { useSkill, useUpdateSkill } from '@omnara/react'
import type { Skill, SkillFile } from '@omnara/sdk'
import { type ReactNode, useState } from 'react'

import { Upload } from '@/components/icons'
import { LazySkillMdEditor } from '@/components/skills/LazySkillMdEditor'
import { SkillFileTree } from '@/components/skills/SkillFileTree'
import { SkillRowActions } from '@/components/skills/SkillRowActions'
import { UploadSkillVersionDialog } from '@/components/skills/UploadSkillVersionDialog'
import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { checkSkillMd } from '@/lib/skill-bundles'
import { errorMessage } from '@/lib/submit-status'

export interface SkillPermissions {
  /** Edit, upload new revisions and delete. */
  canManage: boolean
  /** Share with other projects. */
  canGrant: boolean
}

const editorClass = 'h-[60vh]'

/** A skill's page: its SKILL.md (editable when the viewer can manage it) and its files. */
export function SkillView({
  orgId,
  skillId,
  permissions,
  backLink,
  onRemoved,
}: {
  orgId: string
  skillId: string
  permissions: (skill: Skill) => SkillPermissions
  /** Shown when the skill can't be loaded. */
  backLink: ReactNode
  onRemoved: () => void
}) {
  const detail = useSkill(orgId, skillId)

  if (detail.isPending) {
    return (
      <div className="flex flex-col gap-6">
        <Skeleton className="h-7 w-48" />
        <Skeleton className={editorClass} />
      </div>
    )
  }
  if (detail.isError) {
    return (
      <Empty className="rounded-xl border">
        <EmptyHeader>
          <EmptyDescription>
            This skill doesn&rsquo;t exist, was deleted, or isn&rsquo;t visible to you.
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent>{backLink}</EmptyContent>
      </Empty>
    )
  }
  const skill = detail.data
  const { canManage, canGrant } = permissions(skill)

  return (
    <SkillPage
      key={skill.revision_id}
      orgId={orgId}
      skill={skill}
      canManage={canManage}
      canGrant={canGrant}
      onRemoved={onRemoved}
    />
  )
}

/** Unsaved SKILL.md edits and saving them as a new revision. */
function useSkillMdDraft(orgId: string, skill: Skill) {
  const updateSkill = useUpdateSkill(orgId)
  const [draftMd, setDraftMd] = useState<string>()
  const currentMd = skill.skill_md ?? ''
  const value = draftMd ?? currentMd
  const check = checkSkillMd(value, skill.name)
  const dirty = draftMd !== undefined && draftMd !== currentMd
  return {
    value,
    check,
    dirty,
    saving: updateSkill.isPending,
    error: updateSkill.isError ? errorMessage(updateSkill.error, 'Could not update skill') : null,
    change: (next: string) => {
      setDraftMd(next)
      updateSkill.reset()
    },
    discard: () => {
      setDraftMd(undefined)
      updateSkill.reset()
    },
    save: () => {
      if (!dirty || !check.ok) return
      updateSkill.mutate(
        { skillID: skill.id, body: { skill_md: draftMd } },
        {
          onSuccess: () => {
            setDraftMd(undefined)
          },
        },
      )
    },
  }
}

function SkillPage({
  orgId,
  skill,
  canManage,
  canGrant,
  onRemoved,
}: {
  orgId: string
  skill: Skill
  canManage: boolean
  canGrant: boolean
  onRemoved: () => void
}) {
  const draft = useSkillMdDraft(orgId, skill)

  return (
    <div className="flex flex-col gap-6">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="type-title min-w-0 truncate">{skill.name}</h1>
        <div className="flex shrink-0 items-center gap-2">
          {canManage && (
            <>
              {draft.dirty && !draft.saving && (
                <Button size="sm" variant="ghost" onClick={draft.discard}>
                  Discard
                </Button>
              )}
              <UploadVersionButton orgId={orgId} skill={skill} />
              <Button
                size="sm"
                disabled={!draft.dirty || !draft.check.ok}
                loading={draft.saving}
                onClick={draft.save}
              >
                Save
              </Button>
            </>
          )}
          <SkillRowActions
            orgId={orgId}
            skill={skill}
            canDelete={canManage}
            canGrant={canGrant}
            onRemoved={onRemoved}
          />
        </div>
      </header>
      {draft.error && (
        <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
          {draft.error}
        </p>
      )}
      <LazySkillMdEditor
        id={skill.revision_id}
        className={editorClass}
        value={draft.value}
        readOnly={!canManage || draft.saving}
        problem={draft.check.ok ? undefined : draft.check.problem}
        onChange={draft.change}
      />
      <SkillFileList files={skill.files ?? []} />
    </div>
  )
}

function UploadVersionButton({ orgId, skill }: { orgId: string; skill: Skill }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button
        size="sm"
        variant="outline"
        onClick={() => {
          setOpen(true)
        }}
      >
        <Upload aria-hidden="true" />
        Upload new version
      </Button>
      {open && (
        <UploadSkillVersionDialog
          orgId={orgId}
          skill={skill}
          onClose={() => {
            setOpen(false)
          }}
        />
      )}
    </>
  )
}

function SkillFileList({ files }: { files: SkillFile[] }) {
  if (files.length === 0) return null
  return (
    <section className="flex flex-col gap-2">
      <h2 className="type-label">
        Files <span className="text-muted-foreground font-normal">{files.length}</span>
      </h2>
      <SkillFileTree files={files} />
    </section>
  )
}
