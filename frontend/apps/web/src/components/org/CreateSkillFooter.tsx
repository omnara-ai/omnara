import { ProjectShareChips } from '@/components/projects/ProjectShareChips'
import { Button } from '@/components/ui/button'

import type { useSkillShares } from './useSkillShares'

/** The create-skill footer: who to share with on the left, Back and submit on the right. */
export function CreateSkillFooter({
  orgId,
  shares,
  retrying,
  error,
  busy,
  canSubmit,
  submitLabel,
  onBack,
}: {
  orgId: string
  /** Omitted where skills can't be shared, such as project-owned skills. */
  shares?: ReturnType<typeof useSkillShares>
  retrying: boolean
  error?: string
  busy: boolean
  canSubmit: boolean
  submitLabel: string
  onBack?: () => void
}) {
  return (
    <>
      {error && <p className="text-destructive text-sm">{error}</p>}
      <div className="-mx-4 flex flex-wrap items-center gap-3 border-t px-4 pt-4 sm:-mx-6 sm:px-6">
        {shares && (
          <ProjectShareChips
            orgId={orgId}
            isProjectEligible={(project) => project.access.can_manage}
            value={shares.projectIds}
            onChange={shares.setProjectIds}
            failedProjectIds={shares.failedProjectIds}
            disabled={busy}
          />
        )}
        <div className="ml-auto flex gap-2">
          {onBack && !retrying && (
            <Button type="button" variant="outline" disabled={busy} onClick={onBack}>
              Back
            </Button>
          )}
          <Button type="submit" disabled={busy || (!retrying && !canSubmit)} loading={busy}>
            {retrying ? 'Retry sharing' : submitLabel}
          </Button>
        </div>
      </div>
    </>
  )
}
