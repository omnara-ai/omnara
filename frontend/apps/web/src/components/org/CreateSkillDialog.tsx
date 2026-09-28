import { useCreateSkill, useSkillNameLookup } from '@omnara/react'
import type { Skill, SkillOwnerInput } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { CircleCheck, CircleDashed, NoSymbolIcon, XCircleIcon } from '@/components/icons'
import { LazySkillMdEditor } from '@/components/skills/LazySkillMdEditor'
import { SkillSourcePicker } from '@/components/skills/SkillSourcePicker'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldGroup } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  bundleSkillMd,
  bundleSource,
  checkSkillMd,
  type SkillBundle,
  type SkillMdCheck,
  type SkillSource,
  skillSourceName,
} from '@/lib/skill-bundles'
import { errorMessage, settleSubmission } from '@/lib/submit-status'

const SKILL_MD_TEMPLATE = `---
name: my-skill
description: What this skill does and when to use it.
---

`

type UploadOutcome =
  | { phase: 'uploading' }
  | { phase: 'created'; skill: Skill }
  | { phase: 'failed'; message: string }

type ReviewStatus =
  | { kind: 'new' }
  | { kind: 'revision'; existing: Skill }
  | { kind: 'duplicate'; of: string }
  | { kind: 'invalid'; problem: string }

interface ReviewItem {
  bundle: SkillBundle
  status: ReviewStatus
}

interface Review {
  title: string
  items: ReviewItem[]
}

function reviewItems(bundles: SkillBundle[], existing: ReadonlyMap<string, Skill>): ReviewItem[] {
  const firstByName = new Map<string, SkillBundle>()
  return bundles.map((bundle) => {
    if (bundle.problem !== undefined) {
      return { bundle, status: { kind: 'invalid', problem: bundle.problem } }
    }
    const first = firstByName.get(bundle.label)
    if (first) {
      return { bundle, status: { kind: 'duplicate', of: first.sourcePath || first.label } }
    }
    firstByName.set(bundle.label, bundle)
    const skill = existing.get(bundle.label)
    return { bundle, status: skill ? { kind: 'revision', existing: skill } : { kind: 'new' } }
  })
}

function selectionSummary(selection: Review | undefined) {
  if (!selection) return undefined
  const [only, ...rest] = selection.items
  return only && rest.length === 0 ? only.bundle.label : `${selection.items.length} skills`
}

function isUploadable(item: ReviewItem) {
  return item.status.kind === 'new' || item.status.kind === 'revision'
}

function reviewSubmitLabel(
  pendingItems: ReviewItem[],
  outcomes: ReadonlyMap<SkillBundle, UploadOutcome>,
) {
  const count = pendingItems.length
  if (pendingItems.some((item) => outcomes.has(item.bundle))) {
    return count === 1 ? 'Retry upload' : `Retry ${count} uploads`
  }
  return count === 1 ? 'Upload skill' : `Upload ${count} skills`
}

type Selected = { ok: true; review: Review } | { ok: false; message: string }

async function whileFlagged<T>(setFlag: (value: boolean) => void, work: () => Promise<T>) {
  setFlag(true)
  try {
    return await work()
  } finally {
    setFlag(false)
  }
}

async function uploadSequentially(
  items: ReviewItem[],
  upload: (bundle: SkillBundle) => Promise<UploadOutcome>,
  record: (bundle: SkillBundle, outcome: UploadOutcome) => void,
): Promise<Skill[]> {
  const [first, ...rest] = items
  if (!first) return []
  record(first.bundle, { phase: 'uploading' })
  const outcome = await upload(first.bundle)
  record(first.bundle, outcome)
  const created = await uploadSequentially(rest, upload, record)
  return outcome.phase === 'created' ? [outcome.skill, ...created] : created
}

export function CreateSkillDialog({
  open,
  onOpenChange,
  orgId,
  owner,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  owner: SkillOwnerInput
  onCreated?: (skills: Skill[]) => void
}) {
  const createSkill = useCreateSkill(orgId)
  const lookupSkills = useSkillNameLookup(orgId, owner)
  const [tab, setTab] = useState('upload')
  const [pickedName, setPickedName] = useState<string>()
  const [selection, setSelection] = useState<Review>()
  const [sourceError, setSourceError] = useState<string>()
  const [review, setReview] = useState<Review>()
  const [outcomes, setOutcomes] = useState<ReadonlyMap<SkillBundle, UploadOutcome>>(new Map())
  const [draftMd, setDraftMd] = useState(SKILL_MD_TEMPLATE)
  const [draftError, setDraftError] = useState<string>()
  const [preparing, setPreparing] = useState(false)
  const [uploading, setUploading] = useState(false)
  const busy = preparing || uploading
  const pendingItems = (review?.items ?? []).filter(
    (item) => isUploadable(item) && outcomes.get(item.bundle)?.phase !== 'created',
  )
  const draftCheck = checkSkillMd(draftMd)

  function close() {
    setTab('upload')
    setPickedName(undefined)
    setSelection(undefined)
    setSourceError(undefined)
    setReview(undefined)
    setOutcomes(new Map())
    setDraftMd(SKILL_MD_TEMPLATE)
    setDraftError(undefined)
    onOpenChange(false)
  }

  function handleOpenChange(nextOpen: boolean) {
    if (busy) return
    if (nextOpen) onOpenChange(true)
    else close()
  }

  function back() {
    const remaining = (selection?.items ?? []).filter(
      (item) => outcomes.get(item.bundle)?.phase !== 'created',
    )
    setSelection(selection && remaining.length > 0 ? { ...selection, items: remaining } : undefined)
    if (remaining.length === 0) setPickedName(undefined)
    setReview(undefined)
    setOutcomes(new Map())
  }

  async function lookupExisting(bundles: SkillBundle[]) {
    const names = bundles.flatMap((bundle) => (bundle.problem === undefined ? [bundle.label] : []))
    return settleSubmission(() => lookupSkills(names))
  }

  async function prepareSelection(picked: SkillSource, name: string): Promise<Selected> {
    const bundled = await settleSubmission(() => bundleSource(picked))
    if (!bundled.ok) {
      return {
        ok: false,
        message: `Could not read ${name}. Choose a folder, .zip, or .tar.gz archive.`,
      }
    }
    if (bundled.value.length === 0) return { ok: false, message: `No SKILL.md found in ${name}.` }
    const existing = await lookupExisting(bundled.value)
    if (!existing.ok) {
      return {
        ok: false,
        message: errorMessage(existing.error, 'Could not check for existing skills.'),
      }
    }
    return { ok: true, review: { title: name, items: reviewItems(bundled.value, existing.value) } }
  }

  async function selectSource(picked: SkillSource) {
    const name = skillSourceName(picked)
    setPickedName(name)
    setSelection(undefined)
    setSourceError(undefined)
    const selected = await whileFlagged(setPreparing, () => prepareSelection(picked, name))
    if (selected.ok) {
      setSelection(selected.review)
      return
    }
    setPickedName(undefined)
    setSourceError(selected.message)
  }

  async function uploadBundle(bundle: SkillBundle): Promise<UploadOutcome> {
    const result = await settleSubmission(() =>
      createSkill.mutateAsync({ owner, archive: bundle.archive }),
    )
    return result.ok
      ? { phase: 'created', skill: result.value }
      : { phase: 'failed', message: errorMessage(result.error, 'Could not upload skill') }
  }

  function recordOutcome(bundle: SkillBundle, outcome: UploadOutcome) {
    setOutcomes((prev) => new Map([...prev, [bundle, outcome]]))
  }

  async function createOrReview(picked: Review, reportError: (message: string) => void) {
    const [only, ...rest] = picked.items
    if (!only || rest.length > 0 || only.status.kind !== 'new') {
      setReview(picked)
      return
    }
    const outcome = await whileFlagged(setUploading, () => uploadBundle(only.bundle))
    if (outcome.phase === 'failed') {
      reportError(outcome.message)
      return
    }
    if (outcome.phase === 'created') onCreated?.([outcome.skill])
    close()
  }
  async function submitDraft() {
    setDraftError(undefined)
    const bundle = bundleSkillMd(draftMd)
    const existing = await whileFlagged(setPreparing, () => lookupExisting([bundle]))
    if (!existing.ok) {
      setDraftError(errorMessage(existing.error, 'Could not check for existing skills.'))
      return
    }
    await createOrReview(
      { title: 'SKILL.md', items: reviewItems([bundle], existing.value) },
      setDraftError,
    )
  }

  async function submitReview() {
    const created = await whileFlagged(setUploading, () =>
      uploadSequentially(pendingItems, uploadBundle, recordOutcome),
    )
    if (created.length > 0) onCreated?.(created)
    if (created.length === pendingItems.length) close()
  }

  function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy) return
    if (review) void submitReview()
    else if (tab === 'skill-md') void submitDraft()
    else if (selection) void createOrReview(selection, setSourceError)
  }

  const canSubmit = review
    ? pendingItems.length > 0
    : tab === 'skill-md'
      ? draftCheck.ok
      : selection !== undefined

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className={!review && tab === 'skill-md' ? 'sm:max-w-5xl' : undefined}>
        <DialogHeader>
          <DialogTitle>Create skill</DialogTitle>
        </DialogHeader>
        <form onSubmit={submit}>
          <FieldGroup>
            {review ? (
              <div className="flex flex-col gap-3">
                <p className="truncate text-sm font-medium">{review.title}</p>
                <SkillReviewList items={review.items} outcomes={outcomes} />
              </div>
            ) : (
              <CreateSkillSourceTabs
                tab={tab}
                busy={busy}
                preparing={preparing}
                pickedName={pickedName}
                summary={selectionSummary(selection)}
                sourceError={sourceError}
                draftMd={draftMd}
                draftCheck={draftCheck}
                draftError={draftError}
                onTabChange={setTab}
                onSelect={(picked) => {
                  void selectSource(picked)
                }}
                onDraftChange={(nextValue) => {
                  setDraftMd(nextValue)
                  setDraftError(undefined)
                }}
              />
            )}
            <DialogFooter>
              {review && (
                <Button type="button" variant="outline" disabled={busy} onClick={back}>
                  Back
                </Button>
              )}
              <Button type="submit" disabled={busy || !canSubmit} loading={busy}>
                {review ? reviewSubmitLabel(pendingItems, outcomes) : 'Create skill'}
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function CreateSkillSourceTabs({
  tab,
  busy,
  preparing,
  pickedName,
  summary,
  sourceError,
  draftMd,
  draftCheck,
  draftError,
  onTabChange,
  onSelect,
  onDraftChange,
}: {
  tab: string
  busy: boolean
  preparing: boolean
  pickedName: string | undefined
  summary: string | undefined
  sourceError: string | undefined
  draftMd: string
  draftCheck: SkillMdCheck
  draftError: string | undefined
  onTabChange: (tab: string) => void
  onSelect: (source: SkillSource) => void
  onDraftChange: (value: string) => void
}) {
  return (
    <Tabs
      value={tab}
      onValueChange={(nextTab) => {
        if (!busy) onTabChange(nextTab)
      }}
    >
      <TabsList aria-label="Create mode">
        <TabsTrigger value="upload">Upload</TabsTrigger>
        <TabsTrigger value="skill-md">SKILL.md</TabsTrigger>
      </TabsList>
      <TabsContent value="upload" className="flex flex-col gap-4">
        <SkillSourcePicker
          selectedName={pickedName}
          summary={summary}
          busy={preparing}
          disabled={busy}
          onSelect={onSelect}
        />
        {sourceError && (
          <p role="alert" className="text-destructive text-sm">
            {sourceError}
          </p>
        )}
      </TabsContent>
      <TabsContent value="skill-md">
        <Field>
          <LazySkillMdEditor
            id="new-skill"
            className="h-[55vh]"
            value={draftMd}
            readOnly={busy}
            problem={draftCheck.ok ? undefined : draftCheck.problem}
            onChange={onDraftChange}
          />
        </Field>
        {draftError && (
          <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
            {draftError}
          </p>
        )}
      </TabsContent>
    </Tabs>
  )
}

function ReviewIcon({ item, outcome }: { item: ReviewItem; outcome: UploadOutcome | undefined }) {
  if (outcome?.phase === 'uploading') return <Spinner className="text-muted-foreground size-4" />
  if (outcome?.phase === 'created') {
    return <CircleCheck className="text-success size-4" aria-label="Uploaded" />
  }
  if (outcome?.phase === 'failed' || item.status.kind === 'invalid') {
    return <XCircleIcon className="text-destructive size-4" aria-label="Failed" />
  }
  if (item.status.kind === 'duplicate') {
    return <NoSymbolIcon className="text-muted-foreground size-4" aria-label="Skipped" />
  }
  return <CircleDashed className="text-muted-foreground size-4" aria-label="Pending" />
}

function ReviewStatusLabel({ status }: { status: ReviewStatus }) {
  if (status.kind === 'new') return <span className="text-muted-foreground">New</span>
  if (status.kind === 'revision') {
    return (
      <span className="text-warning">
        v{status.existing.revision} → v{status.existing.revision + 1}
      </span>
    )
  }
  return <span className="text-muted-foreground">Skipped</span>
}

function SkillReviewList({
  items,
  outcomes,
}: {
  items: ReviewItem[]
  outcomes: ReadonlyMap<SkillBundle, UploadOutcome>
}) {
  return (
    <ul
      aria-label="Skills to upload"
      className="border-border max-h-80 divide-y overflow-y-auto rounded-lg border"
    >
      {items.map((item) => {
        const outcome = outcomes.get(item.bundle)
        const detail =
          outcome?.phase === 'failed'
            ? outcome.message
            : item.status.kind === 'invalid'
              ? item.status.problem
              : item.status.kind === 'duplicate'
                ? `Duplicate of ${item.status.of}`
                : undefined
        return (
          <li key={item.bundle.sourcePath} className="flex items-start gap-3 px-3 py-2 text-sm">
            <span className="flex h-5 shrink-0 items-center">
              <ReviewIcon item={item} outcome={outcome} />
            </span>
            <span className="flex min-w-0 flex-1 flex-col gap-0.5">
              <span className="truncate font-mono">{item.bundle.label}</span>
              {detail !== undefined && (
                <span
                  className={
                    item.status.kind === 'duplicate' && outcome?.phase !== 'failed'
                      ? 'text-muted-foreground'
                      : 'text-destructive whitespace-pre-wrap'
                  }
                >
                  {detail}
                </span>
              )}
            </span>
            <span className="shrink-0 text-xs leading-5">
              <ReviewStatusLabel status={item.status} />
            </span>
          </li>
        )
      })}
    </ul>
  )
}
