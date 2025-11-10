import { useState, useMemo, useEffect } from 'react'
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
import { X } from 'lucide-react'
import { useAddPrompts } from '@/hooks/useQueue'

interface AddPromptsModalProps {
  instanceId: string
  isOpen: boolean
  onClose: () => void
}

export function AddPromptsModal({ instanceId, isOpen, onClose }: AddPromptsModalProps) {
  const [promptsText, setPromptsText] = useState('')
  const addPromptsMutation = useAddPrompts(instanceId)

  // Parse prompts from textarea (one per line, filter empty lines)
  const prompts = useMemo(() => {
    return promptsText
      .split('\n')
      .map((line) => line.trim())
      .filter((line) => line.length > 0)
  }, [promptsText])

  const handleSubmit = async () => {
    if (prompts.length === 0) return

    try {
      await addPromptsMutation.mutateAsync(prompts)
      setPromptsText('')
      onClose()
    } catch (error) {
      // Error is handled by the mutation's onError
    }
  }

  const handleClose = () => {
    if (!addPromptsMutation.isPending) {
      setPromptsText('')
      onClose()
    }
  }

  const handleRemovePrompt = (index: number) => {
    const lines = promptsText.split('\n')
    lines.splice(index, 1)
    setPromptsText(lines.join('\n'))
  }

  // Keyboard shortcuts
  useEffect(() => {
    if (!isOpen) return

    const handleKeyDown = (e: KeyboardEvent) => {
      // Cmd+Enter (Mac) or Ctrl+Enter (Windows/Linux) to submit
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault()
        if (prompts.length > 0 && !addPromptsMutation.isPending) {
          handleSubmit()
        }
      }
    }

    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [isOpen, prompts.length, addPromptsMutation.isPending])

  return (
    <Dialog open={isOpen} onOpenChange={handleClose}>
      <DialogContent className="sm:max-w-[600px]">
        <DialogHeader>
          <DialogTitle>Add Prompts to Queue</DialogTitle>
          <DialogDescription>
            Enter one prompt per line. These will be sent to the agent automatically
            as it completes each task.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-4">
          <div className="space-y-2">
            <label htmlFor="prompts" className="text-sm font-medium">
              Prompts
            </label>
            <Textarea
              id="prompts"
              placeholder="Write unit tests&#10;Add error handling&#10;Update documentation"
              value={promptsText}
              onChange={(e) => setPromptsText(e.target.value)}
              className="min-h-[200px] font-mono text-sm"
              disabled={addPromptsMutation.isPending}
            />
            <p className="text-sm text-muted-foreground">
              {prompts.length} {prompts.length === 1 ? 'prompt' : 'prompts'}
            </p>
          </div>

          {/* Preview */}
          {prompts.length > 0 && (
            <div className="space-y-2">
              <label className="text-sm font-medium">Preview</label>
              <div className="rounded-md border bg-muted/50 p-3 space-y-2 max-h-[200px] overflow-y-auto">
                {prompts.map((prompt, index) => (
                  <div
                    key={index}
                    className="flex items-start gap-2 text-sm bg-background rounded p-2 group"
                  >
                    <span className="text-muted-foreground font-medium min-w-[24px]">
                      {index + 1}.
                    </span>
                    <span className="flex-1">{prompt}</span>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-6 w-6 opacity-0 group-hover:opacity-100 transition-opacity"
                      onClick={() => handleRemovePrompt(index)}
                      disabled={addPromptsMutation.isPending}
                    >
                      <X className="w-3 h-3" />
                      <span className="sr-only">Remove</span>
                    </Button>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>

        <DialogFooter className="sm:justify-between">
          <p className="text-xs text-muted-foreground">
            Press <kbd className="px-1.5 py-0.5 text-xs border rounded bg-muted">⌘</kbd>+
            <kbd className="px-1.5 py-0.5 text-xs border rounded bg-muted">Enter</kbd> to add
          </p>
          <div className="flex gap-2">
            <Button
              variant="outline"
              onClick={handleClose}
              disabled={addPromptsMutation.isPending}
            >
              Cancel
            </Button>
            <Button
              onClick={handleSubmit}
              disabled={prompts.length === 0 || addPromptsMutation.isPending}
            >
              {addPromptsMutation.isPending ? 'Adding...' : 'Add to Queue'}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
