import type { ApiError } from '@omnara/sdk'
import { useNavigate } from '@tanstack/react-router'

import { InsufficientCreditsMessage } from '@/components/agents/InsufficientCreditsMessage'
import type { SubmitAction } from '@/components/agents/useCreateAgentSubmission'
import { Button } from '@/components/ui/button'
import { statusError, type SubmitStatus } from '@/lib/submit-status'
import { useWebConfig } from '@/lib/web-config'

export function CreateAgentActions({
  projectId,
  status,
  pendingAction,
  launchError,
  canSubmit,
  onCreateProfile,
}: {
  projectId: string
  status: SubmitStatus
  pendingAction: SubmitAction | null
  launchError?: ApiError
  canSubmit: boolean
  onCreateProfile: () => void
}) {
  const navigate = useNavigate()
  const { data: webConfig } = useWebConfig()
  const isSubmitting = status.phase === 'submitting'
  const errorMessage = statusError(status)

  return (
    <div className="bg-sidebar -mx-4 -mb-4 flex flex-col gap-3 border-t px-4 py-3.5 sm:-mx-6 sm:-mb-6 sm:flex-row sm:items-center sm:justify-between sm:gap-4 sm:px-8">
      <Button
        type="button"
        variant="ghost"
        disabled={isSubmitting}
        className="w-full sm:w-auto"
        onClick={() => {
          void navigate({ to: '/projects/$projectId/agents', params: { projectId } })
        }}
      >
        Cancel
      </Button>
      <div className="flex w-full flex-col gap-2 sm:w-auto sm:flex-row sm:items-center sm:gap-4">
        {launchError && webConfig?.billingURL ? (
          <p className="text-destructive whitespace-pre-wrap text-sm" role="alert">
            <InsufficientCreditsMessage billingHref={webConfig.billingHref} />
          </p>
        ) : errorMessage ? (
          <p className="text-destructive whitespace-pre-wrap text-sm">{errorMessage}</p>
        ) : null}
        <Button
          type="button"
          variant="outline"
          disabled={!canSubmit}
          loading={pendingAction === 'profile'}
          className="w-full sm:w-auto"
          onClick={onCreateProfile}
        >
          Create profile
        </Button>
        <Button
          type="submit"
          disabled={!canSubmit}
          loading={pendingAction === 'launch'}
          className="w-full sm:w-auto"
        >
          Create & launch agent
        </Button>
      </div>
    </div>
  )
}
