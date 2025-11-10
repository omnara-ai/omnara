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
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false)
  const [deletingItemId, setDeletingItemId] = useState<string | null>(null)
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
    setDeletingItemId(itemId)
    setDeleteDialogOpen(true)
  }

  const confirmDelete = () => {
    if (deletingItemId) {
      deleteItemMutation.mutate(deletingItemId, {
        onSuccess: () => {
          setDeleteDialogOpen(false)
          setDeletingItemId(null)
        },
      })
    }
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
                <div className="space-y-2 animate-pulse">
                  {[1, 2, 3].map((i) => (
                    <div key={i} className="flex items-start gap-3 p-3 rounded-lg border bg-muted/30">
                      <div className="w-5 h-5 bg-muted rounded" />
                      <div className="w-6 h-6 bg-muted rounded-full" />
                      <div className="flex-1 space-y-2">
                        <div className="h-4 bg-muted rounded w-3/4" />
                        <div className="h-3 bg-muted rounded w-1/2" />
                      </div>
                      <div className="w-16 h-8 bg-muted rounded" />
                    </div>
                  ))}
                </div>
              ) : !queue || queue.length === 0 ? (
                <div className="flex flex-col items-center justify-center py-12 text-center animate-in fade-in duration-300">
                  <div className="rounded-full bg-muted p-4 mb-4">
                    <svg
                      className="w-8 h-8 text-muted-foreground"
                      fill="none"
                      stroke="currentColor"
                      viewBox="0 0 24 24"
                    >
                      <path
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                        d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2"
                      />
                    </svg>
                  </div>
                  <p className="text-sm font-medium text-foreground mb-1">
                    Queue is empty
                  </p>
                  <p className="text-xs text-muted-foreground">
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

            {/* Queue Analytics */}
            {queueStatus && queueStatus.total > 0 && (
              <div className="border-t pt-4 space-y-3 animate-in fade-in slide-in-from-bottom-2 duration-300">
                <div className="text-xs font-medium text-muted-foreground uppercase tracking-wide">
                  Queue Statistics
                </div>

                {/* Progress Bar */}
                <div className="space-y-2">
                  <div className="flex justify-between text-xs">
                    <span className="text-muted-foreground">Progress</span>
                    <span className="font-medium">
                      {queueStatus.sent}/{queueStatus.total} completed
                    </span>
                  </div>
                  <div className="h-2 bg-muted rounded-full overflow-hidden">
                    <div
                      className="h-full bg-primary transition-all duration-500 ease-out"
                      style={{
                        width: `${(queueStatus.sent / queueStatus.total) * 100}%`,
                      }}
                    />
                  </div>
                </div>

                {/* Stats Grid */}
                <div className="grid grid-cols-3 gap-2 text-xs">
                  <div className="bg-muted/50 rounded-lg p-2 text-center">
                    <div className="font-semibold text-sm">{queueStatus.pending}</div>
                    <div className="text-muted-foreground">Pending</div>
                  </div>
                  <div className="bg-green-50 dark:bg-green-950/30 rounded-lg p-2 text-center">
                    <div className="font-semibold text-sm text-green-700 dark:text-green-400">
                      {queueStatus.sent}
                    </div>
                    <div className="text-muted-foreground">Sent</div>
                  </div>
                  {queueStatus.failed > 0 && (
                    <div className="bg-red-50 dark:bg-red-950/30 rounded-lg p-2 text-center">
                      <div className="font-semibold text-sm text-red-700 dark:text-red-400">
                        {queueStatus.failed}
                      </div>
                      <div className="text-muted-foreground">Failed</div>
                    </div>
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

      {/* Delete Confirmation Dialog */}
      <AlertDialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete this prompt?</AlertDialogTitle>
            <AlertDialogDescription>
              This prompt will be permanently removed from the queue.
              This action cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => setDeletingItemId(null)}>
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={confirmDelete}
              className="bg-destructive hover:bg-destructive/90"
              disabled={deleteItemMutation.isPending}
            >
              {deleteItemMutation.isPending ? 'Deleting...' : 'Delete'}
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
