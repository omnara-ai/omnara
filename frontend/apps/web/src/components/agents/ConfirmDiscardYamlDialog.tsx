import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

export function ConfirmDiscardYamlDialog({
  open,
  onOpenChange,
  onConfirm,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: () => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Discard source edits?</DialogTitle>
          <DialogDescription>
            This returns to the previous Builder configuration and discards the current source
            edits.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            Keep editing source
          </Button>
          <Button variant="destructive" onClick={onConfirm}>
            Discard edits
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
