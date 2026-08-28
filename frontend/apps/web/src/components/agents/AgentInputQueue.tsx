import {
  closestCenter,
  DndContext,
  type DragEndEvent,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
} from '@dnd-kit/core'
import {
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import type { AgentInputBacklogItem, UseAgentChatResult } from '@omnara/react'
import { GripVertical, X } from 'lucide-react'

import { errorMessage } from '@/lib/submit-status'

function inputPreview(input: AgentInputBacklogItem) {
  if (input.delivery_mode === 'optimistic') return input.text
  const blocks = input.content_blocks?.filter((block) => block.metadata?.omnara_hidden !== 'true')
  const text = blocks
    ?.flatMap((block) =>
      block.type === 'text'
        ? [
            (typeof block.metadata?.omnara_display_text === 'string'
              ? block.metadata.omnara_display_text
              : block.text
            ).trim(),
          ]
        : [],
    )
    .filter(Boolean)
    .join('\n')
  if (text != null && text !== '') return text
  return blocks?.some((block) => block.type === 'media_ref') ? 'Attachment' : 'Message'
}

export function AgentInputQueue({
  backlog,
  canOperate,
  canSendNow,
}: {
  backlog: UseAgentChatResult['inputBacklog']
  canOperate: boolean
  canSendNow: boolean
}) {
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )
  const inputs = backlog.inputs
  const queuedInputs = inputs.filter((input) => input.delivery_mode === 'queued')
  const waitingInputs = inputs.filter((input) => input.delivery_mode !== 'steering')
  const queuedIndexes = new Map(waitingInputs.map((input, index) => [input.id, index]))

  if (inputs.length === 0) return null

  async function handleDragEnd({ active, over }: DragEndEvent) {
    if (over == null || active.id === over.id) return
    const oldIndex = queuedInputs.findIndex((input) => input.id === active.id)
    const newIndex = queuedInputs.findIndex((input) => input.id === over.id)
    if (oldIndex < 0 || newIndex < 0) return
    await backlog
      .move({
        inputID: String(active.id),
        anchorInputID: String(over.id),
        position: oldIndex < newIndex ? 'after' : 'before',
      })
      .catch((error: unknown) => {
        window.alert(errorMessage(error, 'Could not reorder this message'))
      })
  }

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      onDragEnd={(event) => void handleDragEnd(event)}
    >
      <SortableContext
        items={inputs.map((input) => input.id)}
        strategy={verticalListSortingStrategy}
      >
        <section
          aria-label="Waiting messages"
          className="bg-muted/50 divide-border divide-y rounded-t-xl border border-b-0 [&~form]:rounded-t-none"
        >
          {inputs.map((input) => {
            const queueIndex = queuedIndexes.get(input.id) ?? -1
            return (
              <QueueRow
                key={input.id}
                input={input}
                queueIndex={queueIndex}
                showReorderHandle={inputs.length >= 2}
                reorderable={input.delivery_mode === 'queued' && queuedInputs.length >= 2}
                canOperate={canOperate}
                canSendNow={canSendNow}
                actionPending={backlog.actionPending}
                onPromote={() => {
                  void backlog.promote(input.id).catch((error: unknown) => {
                    window.alert(errorMessage(error, 'Could not send this message now'))
                  })
                }}
                onCancel={() => {
                  void backlog.cancel(input.id).catch((error: unknown) => {
                    window.alert(errorMessage(error, 'Could not remove this message'))
                  })
                }}
              />
            )
          })}
        </section>
      </SortableContext>
    </DndContext>
  )
}

function QueueRow({
  input,
  queueIndex,
  showReorderHandle,
  reorderable,
  canOperate,
  canSendNow,
  actionPending,
  onPromote,
  onCancel,
}: {
  input: AgentInputBacklogItem
  queueIndex: number
  showReorderHandle: boolean
  reorderable: boolean
  canOperate: boolean
  canSendNow: boolean
  actionPending: boolean
  onPromote: () => void
  onCancel: () => void
}) {
  const pending = input.delivery_mode === 'optimistic'
  const sending = input.delivery_mode === 'steering'
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: input.id,
    disabled: pending || !reorderable || !canOperate || actionPending,
  })

  return (
    <div
      ref={setNodeRef}
      className={`relative flex min-w-0 items-center gap-2 px-3 py-2 ${isDragging ? 'bg-background shadow-sm' : ''}`}
      style={{
        transform: CSS.Transform.toString(transform && { ...transform, x: 0 }),
        transition,
        opacity: isDragging ? 0.7 : undefined,
        zIndex: isDragging ? 1 : undefined,
      }}
    >
      {showReorderHandle && (
        <button
          type="button"
          aria-label={
            sending ? 'Reorder sending message' : `Reorder queued message ${queueIndex + 1}`
          }
          title="Drag to reorder"
          disabled={pending || !reorderable || !canOperate || actionPending}
          className={`text-muted-foreground hover:text-foreground cursor-grab touch-none rounded p-1.5 transition-colors active:cursor-grabbing disabled:pointer-events-none disabled:opacity-50 ${reorderable ? '' : 'invisible'}`}
          {...attributes}
          {...listeners}
        >
          <GripVertical className="size-3.5" />
        </button>
      )}
      <span className="text-muted-foreground w-8 shrink-0 text-xs font-medium tabular-nums">
        {sending ? 'Now' : queueIndex === 0 ? 'Next' : queueIndex + 1}
      </span>
      <p className="text-foreground min-w-0 flex-1 truncate text-sm">{inputPreview(input)}</p>
      {sending || pending ? (
        <button
          type="button"
          disabled
          className="text-muted-foreground w-16 shrink-0 rounded py-1 text-xs font-medium disabled:opacity-50"
        >
          Sending…
        </button>
      ) : canSendNow ? (
        <button
          type="button"
          disabled={actionPending}
          className="text-muted-foreground hover:text-primary w-16 shrink-0 rounded py-1 text-xs font-medium transition-colors disabled:pointer-events-none disabled:opacity-50"
          onClick={onPromote}
        >
          Send now
        </button>
      ) : (
        <span className="w-16 shrink-0" />
      )}
      <button
        type="button"
        aria-label={sending ? 'Remove sending message' : 'Remove queued message'}
        disabled={pending || !canOperate || actionPending || sending}
        className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive shrink-0 rounded p-1 transition-colors disabled:pointer-events-none disabled:opacity-50"
        onClick={onCancel}
      >
        <X className="size-3.5" />
      </button>
    </div>
  )
}
