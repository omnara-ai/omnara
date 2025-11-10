import { ClipboardList, Loader2 } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

interface QueueStatusBadgeProps {
  pendingCount: number
  onClick: () => void
  className?: string
}

export function QueueStatusBadge({ pendingCount, onClick, className }: QueueStatusBadgeProps) {
  if (pendingCount === 0) return null

  return (
    <Button
      variant="outline"
      size="sm"
      onClick={onClick}
      className={cn('gap-2 h-8', className)}
    >
      <ClipboardList className="w-4 h-4" />
      <span className="text-xs font-medium">
        {pendingCount} queued
      </span>
    </Button>
  )
}
