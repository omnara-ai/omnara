import { useState } from 'react'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import { AlertCircle, Plus, Trash2, Loader2 } from 'lucide-react'
import { QueueItem } from './QueueItem'
import { AddPromptsModal } from './AddPromptsModal'
import {
  useQueue,
  useQueueStatus,
  useDeleteQueueItem,
  useClearQueue,
  useReorderQueue,
} from '@/hooks/useQueue'
import {
  DndContext,
  closestCenter,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  DragEndEvent,
} from '@dnd-kit/core'
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable'
import { PromptQueueStatus } from '@/types/dashboard'
import { Alert, AlertDescription } from '@/components/ui/alert'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { EditPromptModal } from './EditPromptModal'

interface PromptQueuePanelProps {
  instanceId: string
  isOpen: boolean
  onClose: () => void
}

export function PromptQueuePanel({ instanceId, isOpen, onClose }: PromptQueuePanelProps) {
  const [isAddModalOpen, setIsAddModalOpen] = useState(false)
  const [isClearDialogOpen, setIsClearDialogOpen] = useState(false)
  const [editingItemId, setEditingItemId] = useState<string | null>(null)
  const [editingItemText, setEditingItemText] = useState('')

  const { data: queue, isLoading: isLoadingQueue } = useQueue(
    instanceId,
    PromptQueueStatus.PENDING
  )
  const { data: queueStatus } = useQueueStatus(instanceId)
  const deleteItemMutation = useDeleteQueueItem(instanceId)
  const clearQueueMutation = useClearQueue(instanceId)
  const reorderMutation = useReorderQueue(instanceId)

  const sensors = useSensors(
    useSensor(PointerSensor),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    })
  )

  const handleDelete = (itemId: string) => {
    deleteItemMutation.mutate(itemId)
  }

  const handleEdit = (itemId: string) => {
    const item = queue?.find((q) => q.id === itemId)
    if (item) {
      setEditingItemId(itemId)
      setEditingItemText(item.prompt_text)
    }
  }

  const handleCloseEditModal = () => {
    setEditingItemId(null)
    setEditingItemText('')
  }

  const handleClearQueue = () => {
    clearQueueMutation.mutate(undefined, {
      onSuccess: () => {
        setIsClearDialogOpen(false)
      },
    })
  }

  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event

    if (!over || !queue || active.id === over.id) {
      return
    }

    const oldIndex = queue.findIndex((item) => item.id === active.id)
    const newIndex = queue.findIndex((item) => item.id === over.id)

    if (oldIndex !== -1 && newIndex !== -1) {
      const reorderedQueue = arrayMove(queue, oldIndex, newIndex)
      const queueItemIds = reorderedQueue.map((item) => item.id)
      reorderMutation.mutate(queueItemIds)
    }
  }

  const pendingCount = queueStatus?.pending ?? 0

  return (
    <>
      <Sheet open={isOpen} onOpenChange={onClose}>
        <SheetContent className="w-full sm:max-w-[500px]">
          <SheetHeader>
            <SheetTitle>Prompt Queue</SheetTitle>
            <SheetDescription>
              {pendingCount > 0
                ? `${pendingCount} ${pendingCount === 1 ? 'prompt' : 'prompts'} pending`
                : 'No prompts in queue'}
            </SheetDescription>
          </SheetHeader>

          <div className="flex flex-col gap-4 mt-6">
            {/* Actions */}
            <div className="flex gap-2">
              <Button
                onClick={() => setIsAddModalOpen(true)}
                className="flex-1"
                size="sm"
              >
                <Plus className="w-4 h-4 mr-2" />
                Add Prompts
              </Button>
              {pendingCount > 0 && (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setIsClearDialogOpen(true)}
                  disabled={clearQueueMutation.isPending}
                >
                  <Trash2 className="w-4 h-4 mr-2" />
                  Clear All
                </Button>
              )}
            </div>

            {/* Info Alert */}
            <Alert>
              <AlertCircle className="h-4 w-4" />
              <AlertDescription className="text-xs">
                Prompts will be sent automatically as the agent completes each task
              </AlertDescription>
            </Alert>

            {/* Queue List */}
            <ScrollArea className="flex-1 h-[calc(100vh-300px)]">
              {isLoadingQueue ? (
                <div className="flex items-center justify-center py-12">
                  <Loader2 className="w-6 h-6 animate-spin text-muted-foreground" />
                </div>
              ) : !queue || queue.length === 0 ? (
                <div className="flex flex-col items-center justify-center py-12 text-center">
                  <p className="text-sm text-muted-foreground">
                    No prompts in queue
                  </p>
                  <p className="text-xs text-muted-foreground mt-1">
                    Click "Add Prompts" to queue tasks for your agent
                  </p>
                </div>
              ) : (
                <DndContext
                  sensors={sensors}
                  collisionDetection={closestCenter}
                  onDragEnd={handleDragEnd}
                >
                  <SortableContext
                    items={queue.map((item) => item.id)}
                    strategy={verticalListSortingStrategy}
                  >
                    <div className="space-y-2">
                      {queue.map((item) => (
                        <QueueItem
                          key={item.id}
                          item={item}
                          onEdit={handleEdit}
                          onDelete={handleDelete}
                        />
                      ))}
                    </div>
                  </SortableContext>
                </DndContext>
              )}
            </ScrollArea>

            {/* Stats */}
            {queueStatus && (queueStatus.sent > 0 || queueStatus.failed > 0) && (
              <div className="border-t pt-3 text-xs text-muted-foreground">
                <div className="flex justify-between">
                  <span>Sent: {queueStatus.sent}</span>
                  {queueStatus.failed > 0 && (
                    <span className="text-red-600">Failed: {queueStatus.failed}</span>
                  )}
                </div>
              </div>
            )}
          </div>
        </SheetContent>
      </Sheet>

      {/* Add Prompts Modal */}
      <AddPromptsModal
        instanceId={instanceId}
        isOpen={isAddModalOpen}
        onClose={() => setIsAddModalOpen(false)}
      />

      {/* Clear Confirmation Dialog */}
      <AlertDialog open={isClearDialogOpen} onOpenChange={setIsClearDialogOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Clear all prompts?</AlertDialogTitle>
            <AlertDialogDescription>
              This will remove all {pendingCount} pending prompt{pendingCount !== 1 ? 's' : ''} from the queue.
              This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleClearQueue}
              className="bg-destructive hover:bg-destructive/90"
            >
              Clear Queue
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Edit Prompt Modal */}
      <EditPromptModal
        instanceId={instanceId}
        itemId={editingItemId}
        initialText={editingItemText}
        isOpen={!!editingItemId}
        onClose={handleCloseEditModal}
      />
    </>
  )
}
