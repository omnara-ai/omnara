import { useState } from 'react'

import { NewProjectDialog } from '@/components/projects/NewProjectDialog'
import { Button } from '@/components/ui/button'

export function NewProjectButton({
  orgId,
  label = 'New project',
}: {
  orgId: string
  label?: string
}) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button
        size="sm"
        onClick={() => {
          setOpen(true)
        }}
      >
        {label}
      </Button>
      <NewProjectDialog open={open} onOpenChange={setOpen} orgId={orgId} />
    </>
  )
}
