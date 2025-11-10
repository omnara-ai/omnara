import { GripVertical, Pencil, Trash2, Check, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { PromptQueueItem, PromptQueueStatus } from '@/types/dashboard'
import { cn } from '@/lib/utils'
import { useSortable } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'

interface QueueItemProps {
  item: PromptQueueItem
  onEdit: (id: string) => void
  onDelete: (id: string) => void
}

export function QueueItem({ item, onEdit, onDelete }: QueueItemProps) {
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: item.id })
  const truncateText = (text: string, maxLength: number = 80) => {
    if (text.length <= maxLength) return text
    return text.slice(0, maxLength) + '...'
  }

  const getStatusColor = () => {
    switch (item.status) {
      case PromptQueueStatus.PENDING:
        return 'bg-background border-border'
      case PromptQueueStatus.SENT:
        return 'bg-muted text-muted-foreground'
      case PromptQueueStatus.FAILED:
        return 'bg-red-50 border-red-500 dark:bg-red-950'
      default:
        return 'bg-background border-border'
    }
  }

  const getStatusIcon = () => {
    switch (item.status) {
      case PromptQueueStatus.SENT:
        return <Check className="w-4 h-4 text-green-600" />
      case PromptQueueStatus.FAILED:
        return <Trash2 className="w-4 h-4 text-red-600" />
      default:
        return null
    }
  }

  const isPending = item.status === PromptQueueStatus.PENDING

  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
  }

  return (
    <div
      ref={setNodeRef}
      style={style}
      className={cn(
        'group relative flex items-start gap-3 p-3 rounded-lg border transition-all',
        getStatusColor(),
        isDragging && 'opacity-50 ring-2 ring-primary',
        isPending && 'hover:shadow-md hover:border-primary/50'
      )}
    >
      {/* Drag Handle */}
      {isPending && (
        <div
          {...attributes}
          {...listeners}
          className="flex-shrink-0 cursor-grab active:cursor-grabbing text-muted-foreground hover:text-foreground"
        >
          <GripVertical className="w-5 h-5" />
        </div>
      )}

      {/* Position Badge */}
      <div className="flex-shrink-0">
        <div className="flex items-center justify-center w-6 h-6 rounded-full bg-muted text-xs font-medium">
          {item.position + 1}
        </div>
      </div>

      {/* Content */}
      <div className="flex-1 min-w-0">
        <p className={cn(
          'text-sm',
          item.status === PromptQueueStatus.SENT && 'line-through'
        )}>
          {truncateText(item.prompt_text)}
        </p>

        {item.error_message && (
          <p className="text-xs text-red-600 dark:text-red-400 mt-1">
            Error: {item.error_message}
          </p>
        )}
      </div>

      {/* Status Icon */}
      {getStatusIcon() && (
        <div className="flex-shrink-0">
          {getStatusIcon()}
        </div>
      )}

      {/* Actions */}
      {isPending && (
        <div className="flex-shrink-0 flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 text-primary hover:bg-primary/10"
                  onClick={() => onEdit(item.id)}
                >
                  <Pencil className="w-4 h-4" />
                  <span className="sr-only">Edit prompt</span>
                </Button>
              </TooltipTrigger>
              <TooltipContent>Edit prompt</TooltipContent>
            </Tooltip>
          </TooltipProvider>

          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 text-destructive hover:bg-destructive/10"
                  onClick={() => onDelete(item.id)}
                >
                  <Trash2 className="w-4 h-4" />
                  <span className="sr-only">Delete prompt</span>
                </Button>
              </TooltipTrigger>
              <TooltipContent>Delete prompt</TooltipContent>
            </Tooltip>
          </TooltipProvider>
        </div>
      )}
    </div>
  )
}
