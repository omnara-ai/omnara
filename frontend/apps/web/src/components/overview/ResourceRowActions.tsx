import { Ellipsis } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

export function ResourceRowActions({
  onEdit,
  onGrant,
  onDelete,
  grantLabel = 'Grant to project',
  deleteLabel = 'Delete',
}: {
  onEdit?: () => void
  onGrant?: () => void
  onDelete?: () => void
  grantLabel?: string
  deleteLabel?: string
}) {
  if (!onEdit && !onGrant && !onDelete) return null

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Row actions">
          <Ellipsis />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {onEdit && <DropdownMenuItem onSelect={onEdit}>Edit</DropdownMenuItem>}
        {onGrant && <DropdownMenuItem onSelect={onGrant}>{grantLabel}</DropdownMenuItem>}
        {onDelete && (
          <DropdownMenuItem variant="destructive" onSelect={onDelete}>
            {deleteLabel}
          </DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
