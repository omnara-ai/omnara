import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { errorMessage } from '@/lib/submit-status'

export function ModelDiscoveryFailureStep({
  deleting,
  providerCreated,
  onBack,
  onContinue,
}: {
  deleting: boolean
  providerCreated: boolean
  onBack: () => Promise<void>
  onContinue: () => void
}) {
  const [deleteError, setDeleteError] = useState('')

  async function back() {
    setDeleteError('')
    try {
      await onBack()
    } catch (error) {
      setDeleteError(errorMessage(error, 'Could not delete the model provider'))
    }
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>Unable to fetch models</DialogTitle>
        <DialogDescription>
          {providerCreated
            ? 'The model provider was created, but its available models could not be fetched.'
            : 'The model provider already exists, but its available models could not be fetched.'}
        </DialogDescription>
      </DialogHeader>
      <div role="alert" className="text-sm text-amber-600 dark:text-amber-500">
        <p className="font-medium">Warning: unable to fetch available models. This might mean:</p>
        <ul className="mt-2 list-disc space-y-1 pl-5">
          <li>Your API token is expired or invalid</li>
          <li>Your API base URL is invalid</li>
          <li>This API endpoint doesn&apos;t support listing models</li>
        </ul>
      </div>
      {deleteError && <p className="text-destructive text-sm">{deleteError}</p>}
      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          disabled={deleting}
          loading={deleting}
          onClick={() => {
            void back()
          }}
        >
          Back
        </Button>
        <Button type="button" disabled={deleting} onClick={onContinue}>
          Continue
        </Button>
      </DialogFooter>
    </>
  )
}
