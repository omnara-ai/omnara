import { useState, useEffect } from 'react'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { useUpdateQueueItem } from '@/hooks/useQueue'

interface EditPromptModalProps {
  instanceId: string
  itemId: string | null
  initialText: string
  isOpen: boolean
  onClose: () => void
}

export function EditPromptModal({
  instanceId,
  itemId,
  initialText,
  isOpen,
  onClose,
}: EditPromptModalProps) {
  const [promptText, setPromptText] = useState(initialText)
  const updateMutation = useUpdateQueueItem(instanceId)

  // Update local state when initialText changes
  useEffect(() => {
    if (isOpen) {
      setPromptText(initialText)
    }
  }, [initialText, isOpen])

  const handleSubmit = async () => {
    if (!itemId || promptText.trim().length === 0) return

    try {
      await updateMutation.mutateAsync({
        queueItemId: itemId,
        promptText: promptText.trim(),
      })
      onClose()
    } catch (error) {
      // Error is handled by the mutation's onError
    }
  }

  const handleClose = () => {
    if (!updateMutation.isPending) {
      setPromptText(initialText)
      onClose()
    }
  }

  const isValid = promptText.trim().length > 0
  const charCount = promptText.length

  return (
    <Dialog open={isOpen} onOpenChange={handleClose}>
      <DialogContent className="sm:max-w-[600px]">
        <DialogHeader>
          <DialogTitle>Edit Prompt</DialogTitle>
          <DialogDescription>
            Update the prompt text. Changes will be reflected in the queue immediately.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-4">
          <div className="space-y-2">
            <label htmlFor="prompt" className="text-sm font-medium">
              Prompt Text
            </label>
            <Textarea
              id="prompt"
              placeholder="Enter your prompt..."
              value={promptText}
              onChange={(e) => setPromptText(e.target.value)}
              className="min-h-[150px] font-mono text-sm"
              disabled={updateMutation.isPending}
              autoFocus
            />
            <div className="flex justify-between text-sm text-muted-foreground">
              <span>{charCount} characters</span>
              {!isValid && charCount > 0 && (
                <span className="text-destructive">Prompt cannot be empty</span>
              )}
            </div>
          </div>

          {/* Preview */}
          {isValid && (
            <div className="space-y-2">
              <label className="text-sm font-medium">Preview</label>
              <div className="rounded-md border bg-muted/50 p-3">
                <p className="text-sm whitespace-pre-wrap">{promptText.trim()}</p>
              </div>
            </div>
          )}
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            onClick={handleClose}
            disabled={updateMutation.isPending}
          >
            Cancel
          </Button>
          <Button
            onClick={handleSubmit}
            disabled={!isValid || updateMutation.isPending}
          >
            {updateMutation.isPending ? 'Saving...' : 'Save Changes'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
