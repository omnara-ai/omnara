import { type SkillUpload, useCreateSkills, useSkillNameLookup } from '@omnara/react'
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
  bundleSource,
  checkSkillMd,
  readSkillSource,
  type SkillBundle,
  type SkillMdCheck,
  type SkillSource,
  skillSourceName,
} from '@/lib/skill-bundles'
import { skillOwnerLabel } from '@/lib/skills'
import { errorMessage, settleSubmission } from '@/lib/submit-status'

const SKILL_MD_TEMPLATE = `---
name: my-skill
description: What this skill does and when to use it.
---

`

const UPLOAD_FAILED = 'Could not upload skill'

type ReviewStatus =
  | { kind: 'new' }
  | { kind: 'revision'; existing: Skill }
  | { kind: 'duplicate'; of: string }
  | { kind: 'invalid'; problem: string }

interface ReviewItem {
  bundle: SkillBundle
  status: ReviewStatus
  replacesAttached?: Skill
}

interface Review {
  title: string
  items: ReviewItem[]
}

function reviewItems(
  bundles: SkillBundle[],
  existing: ReadonlyMap<string, Skill>,
  attachedByName: ReadonlyMap<string, Skill>,
): ReviewItem[] {
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
    const attached = attachedByName.get(bundle.label)
    return {
      bundle,
      status: skill ? { kind: 'revision', existing: skill } : { kind: 'new' },
      replacesAttached: attached && attached.id !== skill?.id ? attached : undefined,
    }
  })
}

function selectionSummary(selection: Review | undefined) {
  if (!selection) return undefined
  const [only, ...rest] = selection.items
  return only && rest.length === 0 ? only.bundle.label : `${selection.items.length} skills`
}

function invalidSelectionMessage(bundles: SkillBundle[]) {
  const problems = bundles.flatMap((bundle) =>
    bundle.problem === undefined ? [] : [{ label: bundle.label, problem: bundle.problem }],
  )
  if (problems.length === 0 || problems.length < bundles.length) return undefined
  const [only, ...rest] = problems
  if (only && rest.length === 0) return only.problem
  return problems.map(({ label, problem }) => `${label}: ${problem}`).join('\n')
}

function isUploadable(item: ReviewItem) {
  return item.status.kind === 'new' || item.status.kind === 'revision'
}

function reviewSubmitLabel(
  pendingItems: ReviewItem[],
  outcomes: ReadonlyMap<SkillBundle, SkillUpload>,
) {
  const count = pendingItems.length
  if (pendingItems.some((item) => outcomes.has(item.bundle))) {
    return count === 1 ? 'Retry upload' : `Retry ${count} uploads`
  }
  return count === 1 ? 'Upload skill' : `Upload ${count} skills`
}

function createdSkills(uploads: SkillUpload[]) {
  return uploads.flatMap((upload) => (upload.phase === 'created' ? [upload.skill] : []))
}

type Prepared = { ok: true; review: Review } | { ok: false; message: string }

async function prepareWhile(
  setPreparing: (preparing: boolean) => void,
  load: () => Promise<Prepared>,
) {
  setPreparing(true)
  try {
    return await load()
  } finally {
    setPreparing(false)
  }
}

export function CreateSkillDialog({
  open,
  onOpenChange,
  orgId,
  owner,
  onCreated,
  attachedSkills = [],
  readSource = bundleSource,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  owner: SkillOwnerInput
  onCreated?: (skills: Skill[]) => void
  attachedSkills?: readonly Skill[]
  readSource?: (source: SkillSource) => Promise<SkillBundle[]>
}) {
  const createSkills = useCreateSkills(orgId)
  const lookupSkills = useSkillNameLookup(orgId, owner)
  const [tab, setTab] = useState('upload')
  const [pickedName, setPickedName] = useState<string>()
  const [selection, setSelection] = useState<Review>()
  const [sourceError, setSourceError] = useState<string>()
  const [review, setReview] = useState<Review>()
  const [outcomes, setOutcomes] = useState<ReadonlyMap<SkillBundle, SkillUpload>>(new Map())
  const [draftMd, setDraftMd] = useState(SKILL_MD_TEMPLATE)
  const [draftError, setDraftError] = useState<string>()
  const [preparing, setPreparing] = useState(false)
  const busy = preparing || createSkills.isPending
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

  async function reviewBundles(title: string, bundles: SkillBundle[]): Promise<Prepared> {
    const names = bundles.flatMap((bundle) => (bundle.problem === undefined ? [bundle.label] : []))
    const existing = await settleSubmission(() => lookupSkills(names))
    if (!existing.ok) {
      return {
        ok: false,
        message: errorMessage(existing.error, 'Could not check for existing skills.'),
      }
    }
    const attachedByName = new Map(attachedSkills.map((skill) => [skill.name, skill]))
    return {
      ok: true,
      review: { title, items: reviewItems(bundles, existing.value, attachedByName) },
    }
  }

  async function reviewSource(picked: SkillSource): Promise<Prepared> {
    const name = skillSourceName(picked)
    const bundled = await readSkillSource(picked, readSource)
    if (!bundled.ok) return bundled
    if (bundled.bundles.length === 0) return { ok: false, message: `No SKILL.md found in ${name}.` }
    const problem = invalidSelectionMessage(bundled.bundles)
    if (problem !== undefined) return { ok: false, message: problem }
    return reviewBundles(name, bundled.bundles)
  }

  function showSelection(prepared: Prepared) {
    if (prepared.ok) {
      setSelection(prepared.review)
      return
    }
    setSelection(undefined)
    setPickedName(undefined)
    setSourceError(prepared.message)
  }

  async function back() {
    const remaining = (selection?.items ?? []).flatMap((item) =>
      outcomes.get(item.bundle)?.phase === 'created' ? [] : [item.bundle],
    )
    setReview(undefined)
    setOutcomes(new Map())
    if (!selection || remaining.length === selection.items.length) return
    if (remaining.length === 0) {
      setSelection(undefined)
      setPickedName(undefined)
      return
    }
    showSelection(await prepareWhile(setPreparing, () => reviewBundles(selection.title, remaining)))
  }

  async function selectSource(picked: SkillSource) {
    setPickedName(skillSourceName(picked))
    setSelection(undefined)
    setSourceError(undefined)
    showSelection(await prepareWhile(setPreparing, () => reviewSource(picked)))
  }

  function recordOutcome(bundle: SkillBundle, outcome: SkillUpload) {
    setOutcomes((prev) => new Map([...prev, [bundle, outcome]]))
  }

  async function createOrReview(picked: Review, reportError: (message: string) => void) {
    const [only, ...rest] = picked.items
    if (
      !only ||
      rest.length > 0 ||
      only.status.kind !== 'new' ||
      only.replacesAttached !== undefined
    ) {
      setReview(picked)
      return
    }
    const [upload] = await createSkills.mutateAsync({ owner, archives: [only.bundle.archive] })
    if (upload?.phase === 'failed') {
      reportError(errorMessage(upload.error, UPLOAD_FAILED))
      return
    }
    if (upload?.phase === 'created') onCreated?.([upload.skill])
    close()
  }

  async function submitDraft() {
    setDraftError(undefined)
    const prepared = await prepareWhile(setPreparing, () =>
      reviewSource({ kind: 'skill-md', text: draftMd }),
    )
    if (prepared.ok) await createOrReview(prepared.review, setDraftError)
    else setDraftError(prepared.message)
  }

  async function submitReview() {
    const items = pendingItems
    const uploads = await createSkills.mutateAsync({
      owner,
      archives: items.map((item) => item.bundle.archive),
      onProgress: (index, upload) => {
        const item = items[index]
        if (item) recordOutcome(item.bundle, upload)
      },
    })
    const created = createdSkills(uploads)
    if (created.length > 0) onCreated?.(created)
    if (created.length === items.length) close()
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
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => {
                    void back()
                  }}
                >
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
          <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
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

function ReviewIcon({ item, outcome }: { item: ReviewItem; outcome: SkillUpload | undefined }) {
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
        v{status.existing.revision} → v{status.existing.revision + 1} · replaces all files
      </span>
    )
  }
  return <span className="text-muted-foreground">Skipped</span>
}

function reviewDetail(item: ReviewItem, outcome: SkillUpload | undefined) {
  if (outcome?.phase === 'failed') {
    return {
      text: errorMessage(outcome.error, UPLOAD_FAILED),
      className: 'text-destructive whitespace-pre-wrap',
    }
  }
  if (item.status.kind === 'invalid') {
    return { text: item.status.problem, className: 'text-destructive whitespace-pre-wrap' }
  }
  if (item.status.kind === 'duplicate') {
    return { text: `Duplicate of ${item.status.of}`, className: 'text-muted-foreground' }
  }
  if (item.replacesAttached) {
    return {
      text: `Replaces the attached ${skillOwnerLabel(item.replacesAttached).toLowerCase()} skill.`,
      className: 'text-warning',
    }
  }
  return undefined
}

function SkillReviewList({
  items,
  outcomes,
}: {
  items: ReviewItem[]
  outcomes: ReadonlyMap<SkillBundle, SkillUpload>
}) {
  return (
    <ul
      aria-label="Skills to upload"
      className="border-border max-h-80 divide-y overflow-y-auto rounded-lg border"
    >
      {items.map((item) => {
        const outcome = outcomes.get(item.bundle)
        const detail = reviewDetail(item, outcome)
        return (
          <li key={item.bundle.sourcePath} className="flex items-start gap-3 px-3 py-2 text-sm">
            <span className="flex h-5 shrink-0 items-center">
              <ReviewIcon item={item} outcome={outcome} />
            </span>
            <span className="flex min-w-0 flex-1 flex-col gap-0.5">
              <span className="truncate font-mono">{item.bundle.label}</span>
              {detail && <span className={detail.className}>{detail.text}</span>}
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
