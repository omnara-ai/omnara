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
          <DialogTitle>Discard YAML edits?</DialogTitle>
          <DialogDescription>
            The builder can’t import direct YAML edits. Switching back discards them and regenerates
            the config from the builder’s fields.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
            }}
          >
            Keep editing YAML
          </Button>
          <Button variant="destructive" onClick={onConfirm}>
            Discard edits
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
